// USB DAC mode: a small USB Audio Class 2 driver of our own on top of TinyUSB (Arduino core 3.3, TinyUSB 0.20).
// The core's built-in audio class is compiled without the feedback endpoint an asynchronous DAC needs,
// so this registers as an "application class driver" (usbd_app_driver_get_cb) and builds its
// descriptor through the core's CUSTOM interface.
//
// One function, speaker only:  clock source (4) -> USB streaming input (1) -> feature unit (2, mute and
// volume = the Windows slider) -> speaker output (3). Streaming interface: alt 1 = 16 bit, alt 2 = 24 bit,
// 44.1/48/88.2/96 kHz, isochronous asynchronous OUT with an explicit feedback IN endpoint.
// Endpoints are re-armed in the interrupt (xfer_isr): the next packet is due 1 ms later.
// (Core 2.0.17's USB driver could not do isochronous OUT properly - that is why we moved to core 3.)
#include <Arduino.h>
#include "USB.h"
#include "esp32-hal-tinyusb.h"
#include "device/usbd_pvt.h"
#include "usb_audio.h"

#define ID_IT  1
#define ID_FU  2
#define ID_OT  3
#define ID_CLK 4

static const uint16_t EP_MPS = 582;                     // 97 frames of 24-bit stereo: 96 kHz plus one spare
static const uint16_t AC_LEN = 9 + 8 + 17 + 18 + 12;    // class-specific AudioControl descriptors
static const uint16_t ALT_LEN = 9 + 16 + 6 + 7 + 8 + 7; // one streaming alternate setting
static const uint16_t DESC_LEN = 8 + 9 + AC_LEN + 9 + 2 * ALT_LEN;
static const uint32_t RATES[] = { 44100, 48000, 88200, 96000 };
static const int16_t VOL_MIN = -60 * 256, VOL_RES = 128;   // 1/256 dB units: -60..0 dB in 0.5 dB steps

volatile uint32_t usbRate = 48000;
volatile uint8_t usbBits = 24;
volatile bool usbStreaming = false;
volatile uint32_t usbSoundMs = 0;
volatile float usbGain = 1.0f;
volatile uint32_t usbDropped = 0;

static uint8_t epOut, epFb, itfAc;
static const tusb_desc_endpoint_t *descOut[3], *descFb[3];   // per alternate setting, from the configuration
// diagnostics for /usb
static volatile uint32_t nAlt, nArm, nArmFail, nRx, nRxFail, nRxBytes, nFb, nRate, lastSetRate;
static volatile uint8_t alt = 0;
static uint8_t mute = 0;
static int16_t volume = 0;
static bool allocOk;
static uint32_t nOpen, rxFifo;

// USB controller FIFO registers (DWC2 at 0x60080000), in 32-bit words
#define DWC2_REG(off) (*(volatile uint32_t *)(0x60080000 + (off)))
#define DWC2_GRXFSIZ DWC2_REG(0x024)
#define DWC2_GHWCFG3 DWC2_REG(0x04C)
#define DWC2_DIEPTXF ((volatile uint32_t *)(0x60080000 + 0x104))
static const uint32_t RXFIFO_NEED = 13 + 1 + (EP_MPS + 3) / 4 + 1 + 2 * 7;   // one packet + status + setup
static const uint32_t RXFIFO_MAX = 200;

// ---------- ring of 32-bit stereo frames: written by the USB task, read by the I2S task ----------
static const uint32_t URING = 16384;                     // frames: 170 ms at 96 kHz
static int32_t *uring;
static volatile uint32_t uwr = 0, urd = 0;
static volatile bool uflush = false;                     // reader drops everything at its next look

uint32_t usbAvail() {
  if (uflush) { urd = uwr; uflush = false; }
  return uwr - urd;
}

void usbRead(int32_t *dst, uint32_t n) {
  uint32_t at = urd % URING, first = min(n, URING - at);
  memcpy(dst, uring + at * 2, first * 8);
  if (n > first) memcpy(dst + first * 2, uring, (n - first) * 8);
  urd += n;
}

void usbSkip(uint32_t n) { urd += n; }

uint32_t usbLevel() { return uwr - urd; }

uint32_t usbTargetFrames() { return usbRate * 25 / 1000; }

bool usbConnected() { return tud_mounted() && !tud_suspended(); }

// ---------- feedback: how many frames per 1 ms frame the host should send (16.16, as TinyUSB sends for UAC2) ----------
// Integers only: this runs in the USB interrupt, and the S3 must not touch the FPU there (it crashes).
static int32_t lvlAvg = 0;                               // buffer level, frames * 64
static uint32_t fbNom = 48u << 16, fbVal = 48u << 16;

static void feedbackReset() {
  lvlAvg = (int32_t)usbTargetFrames() * 64;
  fbNom = (uint32_t)(((uint64_t)usbRate << 16) / 1000);
  fbVal = fbNom;
}

// Keep our buffer near the target: below it ask for a bit more, above it for a bit less
// (0.0005 frame/ms per frame of error). The clamp keeps packets within one frame of nominal, as Windows requires.
static void feedbackUpdate() {
  lvlAvg += ((int32_t)(uwr - urd) * 64 - lvlAvg) / 64;
  int32_t corr = ((int32_t)usbTargetFrames() * 64 - lvlAvg) * 33 / 64;   // 16.16
  if (corr > 16384) corr = 16384;
  if (corr < -16384) corr = -16384;
  fbVal = fbNom + corr;
}

// ---------- endpoints ----------
static uint8_t rxBuf[2][EP_MPS] __attribute__((aligned(4)));
static int rxIdx = 0;
static uint32_t fbBuf;

static void armOut(uint8_t rhport, bool isr) {
  if (usbd_edpt_xfer(rhport, epOut, rxBuf[rxIdx], EP_MPS, isr)) nArm++; else nArmFail++;
}

static void armFb(uint8_t rhport, bool isr) {
  fbBuf = fbVal;
  if (usbd_edpt_xfer(rhport, epFb, (uint8_t *)&fbBuf, 4, isr)) nFb++;
}

static void onPacket(const uint8_t *p, uint32_t len) {
  uint32_t fs = usbBits == 16 ? 4 : 6, n = len / fs;
  if (URING - (uwr - urd) < n) {
    // Nobody reads (the PC was quiet and the network has the player): drop, but still look for sound,
    // or the player never notices the PC playing again. The reader starts from fresh data.
    usbDropped += n;
    uflush = true;
    for (uint32_t i = fs / 2 - 1; i < len; i += fs / 2)            // top byte of every sample
      if (p[i] != 0 && p[i] != 0xFF) { usbSoundMs = millis(); break; }
    return;
  }
  bool sound = false;
  uint32_t at = uwr % URING;
  for (uint32_t i = 0; i < n; i++) {
    int32_t *f = uring + ((at + i) % URING) * 2;
    for (int c = 0; c < 2; c++) {
      int32_t v;
      if (fs == 4) { v = (int32_t)((uint32_t)(p[0] | p[1] << 8) << 16); p += 2; }
      else { v = (int32_t)((uint32_t)p[0] << 8 | (uint32_t)p[1] << 16 | (uint32_t)p[2] << 24); p += 3; }
      f[c] = v;
      if (v > (2 << 16) || v < -(2 << 16)) sound = true;        // more than 2 steps of 16-bit: not silence
    }
  }
  uwr += n;
  if (sound) usbSoundMs = millis();
}

static void setAlt(uint8_t rhport, uint8_t a) {
  alt = a <= 2 && descOut[a] && allocOk ? a : 0;
  nAlt++;
  if (alt) {
    usbBits = alt == 1 ? 16 : 24;
    uflush = true;
    feedbackReset();
    usbd_edpt_iso_activate(rhport, descOut[alt]);
    usbd_edpt_iso_activate(rhport, descFb[alt]);
    usbStreaming = true;
    armOut(rhport, false);
    armFb(rhport, false);
  } else {
    usbStreaming = false;                                // the endpoints stay allocated, nothing is re-armed
  }
}

static void setRate(uint32_t r) {
  lastSetRate = r;                                     // what the PC asked, even if we don't have it
  for (uint32_t x : RATES) if (x == r) {
    nRate++;
    if (r != usbRate) { usbRate = r; uflush = true; }
    feedbackReset();

  }
}

static void updateGain() {
  usbGain = mute ? 0.0f : powf(10.0f, volume / 256.0f / 20.0f);
}

// ---------- class driver ----------
static void put16(uint8_t *b, uint16_t v) { b[0] = v; b[1] = v >> 8; }
static void put32(uint8_t *b, uint32_t v) { b[0] = v; b[1] = v >> 8; b[2] = v >> 16; b[3] = v >> 24; }

static void drvInit() {}

static bool drvDeinit() { return true; }

static void drvReset(uint8_t) {
  alt = 0;
  usbStreaming = false;
}

static uint16_t drvOpen(uint8_t rhport, tusb_desc_interface_t const *d, uint16_t maxLen) {
  if (d->bInterfaceClass != 1 || d->bInterfaceSubClass != 1 || d->bInterfaceNumber != itfAc) return 0;
  uint16_t len = DESC_LEN - 8;                           // from the AudioControl interface on (the IAD is before it)
  if (len > maxLen) return 0;
  // isochronous endpoints: buffer space now, configured when the PC picks an alternate setting
  uint8_t curAlt = 0;
  memset(descOut, 0, sizeof(descOut));
  memset(descFb, 0, sizeof(descFb));
  for (const uint8_t *p = (const uint8_t *)d, *end = p + len; p < end; p += p[0]) {
    if (p[1] == TUSB_DESC_INTERFACE) curAlt = p[3];
    if (p[1] != TUSB_DESC_ENDPOINT || curAlt > 2) continue;
    if (p[2] == epOut) descOut[curAlt] = (const tusb_desc_endpoint_t *)p;
    if (p[2] == epFb) descFb[curAlt] = (const tusb_desc_endpoint_t *)p;
  }
  // TinyUSB sizes the RX FIFO for two packets: 322 words for 582 bytes, the S3 has 256 in all, and the
  // allocation fails. So allocate the OUT endpoint small and then grow the RX FIFO ourselves to everything
  // below the TX FIFOs: one packet (175 words) is enough when it comes once per millisecond.
  allocOk = usbd_edpt_iso_alloc(rhport, epOut, 64) && usbd_edpt_iso_alloc(rhport, epFb, 4);
  if (allocOk) {
    uint32_t top = DWC2_DIEPTXF[(epFb & 0x7F) - 1] & 0xFFFF;   // start of the last TX FIFO allocated
    rxFifo = top > RXFIFO_MAX ? RXFIFO_MAX : top;
    if (rxFifo >= RXFIFO_NEED) DWC2_GRXFSIZ = rxFifo; else allocOk = false;
  }
  nOpen++;
  alt = 0;
  usbStreaming = false;
  return len;                    // claim the interface anyway, or the core's own audio class takes it and stalls everything
}

// last control requests and our answers, for /usb
static struct { uint8_t type, req, stage, ok; uint16_t value, index, len; } reqLog[24];
static volatile uint32_t reqN = 0;
static bool drvControlInner(uint8_t rhport, uint8_t stage, tusb_control_request_t const *req);

static bool drvControl(uint8_t rhport, uint8_t stage, tusb_control_request_t const *req) {
  bool ok = drvControlInner(rhport, stage, req);
  if (stage == CONTROL_STAGE_SETUP || !ok) {
    auto &e = reqLog[reqN++ % 24];
    e = { req->bmRequestType, req->bRequest, stage, ok, req->wValue, req->wIndex, req->wLength };
  }
  return ok;
}

static bool drvControlInner(uint8_t rhport, uint8_t stage, tusb_control_request_t const *req) {
  static uint8_t buf[64] __attribute__((aligned(4)));
  uint8_t itf = tu_u16_low(req->wIndex);
  if (req->bmRequestType_bit.type == TUSB_REQ_TYPE_STANDARD) {
    if (stage != CONTROL_STAGE_SETUP) return true;
    if (req->bRequest == TUSB_REQ_SET_INTERFACE) {
      if (itf == itfAc + 1) setAlt(rhport, (uint8_t)req->wValue);
      return tud_control_status(rhport, req);
    }
    if (req->bRequest == TUSB_REQ_GET_INTERFACE) {
      buf[0] = itf == itfAc + 1 ? alt : 0;
      return tud_control_xfer(rhport, req, buf, 1);
    }
    return false;
  }
  if (req->bmRequestType_bit.type != TUSB_REQ_TYPE_CLASS || req->bmRequestType_bit.recipient != TUSB_REQ_RCPT_INTERFACE) return false;
  uint8_t entity = tu_u16_high(req->wIndex), cs = tu_u16_high(req->wValue);
  bool get = req->bmRequestType_bit.direction == TUSB_DIR_IN;
  const uint8_t CUR = 1, RANGE = 2;
  if (stage == CONTROL_STAGE_SETUP) {
    if (!get) {
      bool known = (entity == ID_CLK && cs == 1) || (entity == ID_FU && (cs == 1 || cs == 2));
      if (!known || req->bRequest != CUR || req->wLength > sizeof(buf)) return false;
      return tud_control_xfer(rhport, req, buf, req->wLength);
    }
    uint16_t len = 0;
    if (entity == ID_CLK && cs == 1 && req->bRequest == CUR) { put32(buf, usbRate); len = 4; }
    else if (entity == ID_CLK && cs == 1 && req->bRequest == RANGE) {
      put16(buf, 4);
      for (int i = 0; i < 4; i++) { put32(buf + 2 + i * 12, RATES[i]); put32(buf + 6 + i * 12, RATES[i]); put32(buf + 10 + i * 12, 0); }
      len = 2 + 4 * 12;
    }
    else if (entity == ID_CLK && cs == 2 && req->bRequest == CUR) { buf[0] = 1; len = 1; }   // clock valid
    else if (entity == ID_FU && cs == 1 && req->bRequest == CUR) { buf[0] = mute; len = 1; }
    else if (entity == ID_FU && cs == 2 && req->bRequest == CUR) { put16(buf, volume); len = 2; }
    else if (entity == ID_FU && cs == 2 && req->bRequest == RANGE) {
      put16(buf, 1); put16(buf + 2, VOL_MIN); put16(buf + 4, 0); put16(buf + 6, VOL_RES); len = 8;
    }
    else return false;
    return tud_control_xfer(rhport, req, buf, len);
  }
  if (stage == CONTROL_STAGE_DATA && !get) {
    if (entity == ID_CLK && cs == 1) setRate(buf[0] | buf[1] << 8 | buf[2] << 16 | (uint32_t)buf[3] << 24);
    else if (entity == ID_FU && cs == 1) { mute = buf[0]; updateGain(); }
    else if (entity == ID_FU && cs == 2) {
      volume = (int16_t)(buf[0] | buf[1] << 8);
      if (volume < VOL_MIN) volume = VOL_MIN;
      if (volume > 0) volume = 0;
      updateGain();
    }
  }
  return true;
}

// In the interrupt: re-arm first (the next packet comes in 1 ms), then take the data.
static bool drvXferIsr(uint8_t rhport, uint8_t ep, xfer_result_t result, uint32_t n) {
  if (ep == epOut) {
    uint8_t *got = rxBuf[rxIdx];
    rxIdx ^= 1;
    if (alt) armOut(rhport, true);
    if (result == XFER_RESULT_SUCCESS) { nRx++; nRxBytes = n; } else nRxFail++;
    if (result == XFER_RESULT_SUCCESS && alt) {
      onPacket(got, n);
      feedbackUpdate();
    }
    return true;
  }
  if (ep == epFb) {
    if (alt) armFb(rhport, true);
    return true;
  }
  return false;
}

static bool drvXfer(uint8_t, uint8_t, xfer_result_t, uint32_t) { return true; }

static usbd_class_driver_t const driver = {
  .name = "S3HIFI",
  .init = drvInit,
  .deinit = drvDeinit,
  .reset = drvReset,
  .open = drvOpen,
  .control_xfer_cb = drvControl,
  .xfer_cb = drvXfer,
  .xfer_isr = drvXferIsr,
  .sof = NULL,
};

extern "C" usbd_class_driver_t const *usbd_app_driver_get_cb(uint8_t *count) {
  *count = 1;
  return &driver;
}

// ---------- descriptor (built when USB starts) ----------
static uint16_t descriptor(uint8_t *d, uint8_t *itf) {
  uint8_t ac = *itf, as = ac + 1;
  *itf += 2;
  itfAc = ac;
  uint8_t str = tinyusb_add_string_descriptor("S3 Hi-Fi");
  uint8_t *p = d;
  auto put = [&](std::initializer_list<uint8_t> b) { for (uint8_t x : b) *p++ = x; };
  const uint8_t lo = AC_LEN & 0xFF, hi = AC_LEN >> 8;
  put({ 8, 0x0B, ac, 2, 1, 0, 0x20, str });                            // IAD: audio function, UAC2
  put({ 9, 0x04, ac, 0, 0, 1, 1, 0x20, str });                         // AudioControl interface
  put({ 9, 0x24, 0x01, 0x00, 0x02, 0x01, lo, hi, 0 });                 // header: ADC 2.0, desktop speaker
  put({ 8, 0x24, 0x0A, ID_CLK, 0x03, 0x07, 0, 0 });                    // clock: internal, rate r/w, valid r
  put({ 17, 0x24, 0x02, ID_IT, 0x01, 0x01, 0, ID_CLK, 2, 3, 0, 0, 0, 0, 0, 0, 0 });   // input: USB streaming, L+R
  put({ 18, 0x24, 0x06, ID_FU, ID_IT, 0x0F, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0 });    // master mute + volume
  put({ 12, 0x24, 0x03, ID_OT, 0x01, 0x03, 0, ID_FU, ID_CLK, 0, 0, 0 });              // output: speaker
  put({ 9, 0x04, as, 0, 0, 1, 2, 0x20, str });                         // streaming, alt 0: no bandwidth
  for (uint8_t a = 1; a <= 2; a++) {
    uint8_t sub = a == 1 ? 2 : 3;
    put({ 9, 0x04, as, a, 2, 1, 2, 0x20, str });
    put({ 16, 0x24, 0x01, ID_IT, 0, 1, 1, 0, 0, 0, 2, 3, 0, 0, 0, 0 });  // PCM, 2 channels L+R
    put({ 6, 0x24, 0x02, 1, sub, (uint8_t)(sub * 8) });                  // type I: 2 bytes/16 bit or 3 bytes/24 bit
    put({ 7, 0x05, epOut, 0x05, EP_MPS & 0xFF, EP_MPS >> 8, 1 });        // isochronous, asynchronous, every 1 ms
    put({ 8, 0x25, 0x01, 0, 0, 0, 0, 0 });
    put({ 7, 0x05, epFb, 0x11, 4, 0, 1 });                               // feedback
  }
  return p - d;
}

void usbAudioBegin() {
  uring = (int32_t *)heap_caps_malloc(URING * 8, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
  if (!uring) { Serial.println("USB: no memory"); return; }
  epOut = tinyusb_get_free_out_endpoint();
  epFb = 0x80 | tinyusb_get_free_in_endpoint();
  tinyusb_enable_interface(USB_INTERFACE_CUSTOM, DESC_LEN, descriptor);
  USB.VID(0x303A);
  USB.PID(0x8A51);
  USB.productName("S3 Hi-Fi");
  USB.manufacturerName("S3 Hi-Fi");
  USB.usbPower(100);
  USB.begin();
}

String usbDiag() {
  char b[500];
  snprintf(b, sizeof(b),
           "mounted %d suspended %d alt %u (set %u times) rate %u (set %u) bits %u\n"
           "OUT ep %02X: armed %u, arm failed %u, received %u, last %u bytes, failed %u\n"
           "feedback ep %02X: sent %u, value %.4f frames/ms\n"
           "ring %u frames, dropped %u, gain %.3f, last sound %u ms ago\n",
           tud_mounted(), tud_suspended(), alt, nAlt, usbRate, nRate, usbBits,
           epOut, nArm, nArmFail, nRx, nRxBytes, nRxFail,
           epFb, nFb, fbVal / 65536.0f,
           uwr - urd, usbDropped, usbGain, millis() - usbSoundMs);
  String r = b;
  snprintf(b, sizeof(b), "open %u, alloc %s, rx fifo %u words (need %u), GRXFSIZ %u, fifo depth %u\n"
           "up %u s, last reset reason %d, PC asked rate %u\n",
           nOpen, allocOk ? "ok" : "FAILED", rxFifo, RXFIFO_NEED, DWC2_GRXFSIZ & 0xFFFF, DWC2_GHWCFG3 >> 16,
           millis() / 1000, (int)esp_reset_reason(), lastSetRate);
  r += b;
  r += "requests (type req value index len stage ok):\n";
  uint32_t n = reqN, from = n > 24 ? n - 24 : 0;
  for (uint32_t i = from; i < n; i++) {
    auto &e = reqLog[i % 24];
    char l[64];
    snprintf(l, sizeof(l), "%02X %02X %04X %04X %u st%u %s\n", e.type, e.req, e.value, e.index, e.len, e.stage, e.ok ? "ok" : "STALL");
    r += l;
  }
  return r;
}

void usbReplug() {
  tud_disconnect();
  delay(300);
  tud_connect();
}
