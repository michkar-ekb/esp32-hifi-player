// S3 Hi-Fi player firmware v0.4 — thin client of the S3 Hi-Fi server.
// The server decodes everything to PCM; the player keeps a ~10 s buffer in PSRAM and
// clocks it out over I2S as 32-bit samples (what the ES9038Q2M board expects).
//
//   stream task (core 0): GET /esp/stream?epoch=E&lane=L over 4 connections -> PCM chunks -> ring buffer
//   poll task   (core 0): GET /esp/poll ... 4 times a second -> epoch, play/pause/stop, volume
//   i2s task    (core 1): ring buffer -> volume -> I2S; pause and volume act immediately
//
// A new epoch (new track, seek, stop) means: drop the buffer and reconnect.
// Build: esp32:esp32:esp32s3:PSRAM=opi,FlashSize=16M (core 2.0.17)
#include <WiFi.h>
#include <driver/i2s.h>
#include <math.h>
#include <lwip/sockets.h>
#include <Preferences.h>
#include <WebServer.h>
#include <DNSServer.h>
#include <WiFiUdp.h>
#include "lane.h"

// ---------- settings ----------
// Wi-Fi and the server are set up from a phone: with no saved network (or if it is lost for a minute)
// the player opens the access point "S3 Hi-Fi Setup" with a setup page. The server is found by itself:
// the player broadcasts "S3HIFI?" on UDP 8097 and the server answers. Holding BOOT for 5 s erases the settings.
const char *AP_NAME = "S3 Hi-Fi Setup";
const uint16_t DISCOVERY_PORT = 8097;
String cfgSsid, cfgPass, cfgServer;     // saved in NVS; cfgServer empty = find automatically
char serverIp[40] = "";
volatile uint16_t serverPort = 8097;
volatile bool serverKnown = false;

#define I2S_BCLK 1
#define I2S_LRCK 2
#define I2S_DOUT 42

// ---------- ring buffer in PSRAM: int32 stereo frames ----------
const uint32_t RING_FRAMES = 1 << 19;  // 512k frames = 4 MB, ~11.9 s at 44.1 kHz
int32_t *ring;                          // [frame*2 + ch]
volatile uint64_t wr = 0, rd = 0;       // total frames written / read since boot

struct RateMark { uint64_t frame; uint32_t rate; };
const int MARKS = 32;
RateMark marks[MARKS];
volatile int markHead = 0, markTail = 0;
uint32_t writeRate = 0;                 // rate of the data the stream task writes

// ---------- shared state ----------
volatile uint32_t serverEpoch = 0;      // latest epoch from poll
volatile uint32_t streamEpoch = 0;      // epoch the buffer belongs to
volatile bool flushReq = false;
volatile char playState = 's';          // 'p' play, 'z' pause, 's' stop
volatile int volumePct = 70;
volatile uint64_t played = 0;           // frames played since the last flush
volatile uint32_t outRate = 44100;
volatile bool srcIdle = true;           // stream task has no connection (nothing more coming)

// ---------- I2S ----------
void i2sInit() {
  i2s_config_t cfg = {};
  cfg.mode = (i2s_mode_t)(I2S_MODE_MASTER | I2S_MODE_TX);
  cfg.sample_rate = 44100;
  cfg.bits_per_sample = I2S_BITS_PER_SAMPLE_32BIT;
  cfg.channel_format = I2S_CHANNEL_FMT_RIGHT_LEFT;
  cfg.communication_format = I2S_COMM_FORMAT_STAND_I2S;
  cfg.intr_alloc_flags = ESP_INTR_FLAG_LEVEL1;
  cfg.dma_buf_count = 8;
  cfg.dma_buf_len = 256;
  cfg.tx_desc_auto_clear = true;
  i2s_driver_install(I2S_NUM_0, &cfg, 0, nullptr);
  i2s_pin_config_t pins = {};
  pins.mck_io_num = I2S_PIN_NO_CHANGE;
  pins.bck_io_num = I2S_BCLK;
  pins.ws_io_num = I2S_LRCK;
  pins.data_out_num = I2S_DOUT;
  pins.data_in_num = I2S_PIN_NO_CHANGE;
  i2s_set_pin(I2S_NUM_0, &pins);
}

float volumeGain(int v) {               // 100 = 0 dB, 0.4 dB per step, 0 = mute
  if (v <= 0) return 0;
  return powf(10.0f, -(100 - v) * 0.4f / 20.0f);
}

void i2sTask(void *) {
  const int N = 256;
  static int32_t out[N * 2];
  bool prebuffer = true;
  for (;;) {
    if (flushReq) {
      rd = wr;
      markTail = markHead;
      played = 0;
      prebuffer = true;
      flushReq = false;
    }
    uint64_t avail = wr - rd;
    if (prebuffer && (avail >= outRate / 2 || (srcIdle && avail > 0))) prebuffer = false;
    if (playState == 'p' && !prebuffer && avail > 0) {
      // sample rate change at this point of the stream?
      if (markTail != markHead && marks[markTail].frame <= rd) {
        uint32_t r = marks[markTail].rate;
        markTail = (markTail + 1) % MARKS;
        if (r != outRate) {
          i2s_set_clk(I2S_NUM_0, r, I2S_BITS_PER_SAMPLE_32BIT, I2S_CHANNEL_STEREO);
          outRate = r;
        }
      }
      uint32_t n = avail < N ? (uint32_t)avail : N;
      if (markTail != markHead && marks[markTail].frame > rd && marks[markTail].frame - rd < n)
        n = marks[markTail].frame - rd;
      float g = volumeGain(volumePct);
      for (uint32_t i = 0; i < n; i++) {
        uint32_t k = (uint32_t)((rd + i) % RING_FRAMES) * 2;
        out[i * 2] = (int32_t)(ring[k] * g);
        out[i * 2 + 1] = (int32_t)(ring[k + 1] * g);
      }
      size_t w;
      i2s_write(I2S_NUM_0, out, n * 8, &w, portMAX_DELAY);
      rd += n;
      played += n;
      if (wr == rd && !srcIdle) prebuffer = true;  // underrun: refill a little before continuing
    } else {
      memset(out, 0, sizeof(out));                // keep the clock running: no pops
      size_t w;
      i2s_write(I2S_NUM_0, out, sizeof(out), &w, portMAX_DELAY);
    }
  }
}

// ---------- network helpers ----------
// ---------- network: plain lwIP sockets, big reads, blocking with a short timeout ----------
// (WiFiClient copies everything through a 1.4 KB buffer; at 4+ Mbit/s on four connections
//  that overhead made the player fall behind.)
volatile uint32_t rxBytes = 0;          // total payload bytes received, for the kbit/s report

void laneClose(Lane &L) {
  if (L.fd >= 0) close(L.fd);
  L.fd = -1;
  L.pos = L.len = 0;
}

// Reads exactly n bytes. False on disconnect or when the epoch changes.
bool laneRead(Lane &L, uint8_t *dst, size_t n, uint32_t epoch) {
  while (n) {
    if (L.pos < L.len) {
      size_t k = L.len - L.pos < n ? L.len - L.pos : n;
      memcpy(dst, L.buf + L.pos, k);
      L.pos += k; dst += k; n -= k;
      continue;
    }
    if (serverEpoch != epoch) return false;
    // large request and empty buffer: receive straight into the destination
    uint8_t *to = n >= 2048 ? dst : L.buf;
    size_t cap = n >= 2048 ? n : sizeof(L.buf);
    int r = recv(L.fd, to, cap, 0);
    if (r > 0) {
      rxBytes += r;
      if (to == dst) { dst += r; n -= r; } else { L.pos = 0; L.len = r; }
    } else if (r < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
      continue;                          // timeout: re-check the epoch
    } else {
      return false;                      // closed or error
    }
  }
  return true;
}

bool laneOpen(Lane &L, uint32_t epoch, int lane, int lanes) {
  laneClose(L);
  L.fd = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
  if (L.fd < 0) return false;
  int one = 1;
  setsockopt(L.fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));
  struct timeval tv = { 0, 200000 };     // 200 ms: wake up to notice an epoch change
  setsockopt(L.fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
  struct sockaddr_in a = {};
  a.sin_family = AF_INET;
  a.sin_port = htons(serverPort);
  a.sin_addr.s_addr = inet_addr(serverIp);
  if (connect(L.fd, (struct sockaddr *)&a, sizeof(a)) != 0) { laneClose(L); return false; }
  char req[160];
  int k = snprintf(req, sizeof(req), "GET /esp/stream?epoch=%u&lane=%d&lanes=%d HTTP/1.0\r\nHost: %s\r\n\r\n",
                   epoch, lane, lanes, serverIp);
  if (send(L.fd, req, k, 0) != k) { laneClose(L); return false; }
  uint32_t last4 = 0;                    // skip the HTTP header up to "\r\n\r\n"
  uint8_t b;
  while (last4 != 0x0D0A0D0A) {
    if (!laneRead(L, &b, 1, epoch)) { laneClose(L); return false; }
    last4 = (last4 << 8) | b;
  }
  return true;
}

void pushRate(uint32_t r) {
  if (r == writeRate) return;
  marks[markHead] = { wr, r };
  markHead = (markHead + 1) % MARKS;
  writeRate = r;
}

// The ESP32 TCP window is fixed at 5.7 KB (Arduino core), which caps one connection at ~5 Mbit/s
// on Wi-Fi with 8 ms RTT. So the stream comes over LANES connections: chunk n arrives on lane n % LANES,
// we read them in that order, and while we read one lane the others keep filling their windows.
const int LANES = 4;
volatile bool resyncReq = false;       // a lane broke: ask the server to restart from what was heard

bool readChunkInto(Lane &c, uint32_t epoch, uint32_t seq) {
  uint8_t h[20];
  if (!laneRead(c, h, 20, epoch)) return false;
  if (memcmp(h, "S3P2", 4) != 0) { Serial.println("stream: bad chunk"); return false; }
  uint32_t rate = h[4] | h[5] << 8 | h[6] << 16 | (uint32_t)h[7] << 24;
  uint8_t bits = h[8], ch = h[9];
  uint32_t len = h[12] | h[13] << 8 | h[14] << 16 | (uint32_t)h[15] << 24;
  uint32_t sq = h[16] | h[17] << 8 | h[18] << 16 | (uint32_t)h[19] << 24;
  uint32_t fsz = bits / 8 * ch;
  if (sq != seq) { Serial.printf("stream: seq %u, want %u\n", sq, seq); return false; }
  if ((bits != 16 && bits != 24) || ch < 1 || ch > 2 || len % fsz || len > 8192) { Serial.println("stream: bad format"); return false; }
  static uint8_t buf[8192];
  uint32_t frames = len / fsz;
  while (RING_FRAMES - (wr - rd) < frames) {          // ring full: wait for the I2S side
    if (serverEpoch != epoch) return false;
    vTaskDelay(pdMS_TO_TICKS(5));
  }
  if (!laneRead(c, buf, len, epoch)) return false;
  pushRate(rate);
  const uint8_t *p = buf;
  for (uint32_t i = 0; i < frames; i++) {
    int32_t l, r;
    if (bits == 16) {
      l = (int32_t)((uint32_t)(p[0] | p[1] << 8) << 16);
      p += 2;
      if (ch == 2) { r = (int32_t)((uint32_t)(p[0] | p[1] << 8) << 16); p += 2; } else r = l;
    } else {
      l = (int32_t)((uint32_t)p[0] << 8 | (uint32_t)p[1] << 16 | (uint32_t)p[2] << 24);
      p += 3;
      if (ch == 2) { r = (int32_t)((uint32_t)p[0] << 8 | (uint32_t)p[1] << 16 | (uint32_t)p[2] << 24); p += 3; } else r = l;
    }
    uint32_t k = (uint32_t)((wr + i) % RING_FRAMES) * 2;
    ring[k] = l;
    ring[k + 1] = r;
  }
  wr += frames;
  return true;
}

// All lanes of `epoch`. Returns when the epoch changes or a lane drops.
void streamOnce(uint32_t epoch) {
  static Lane c[LANES];
  bool ok = true;
  for (int l = 0; l < LANES && ok; l++) ok = laneOpen(c[l], epoch, l, LANES);
  if (ok) {
    srcIdle = false;
    for (uint32_t seq = 0; readChunkInto(c[seq % LANES], epoch, seq); seq++) {}
  }
  for (int l = 0; l < LANES; l++) laneClose(c[l]);
  srcIdle = true;
  if (serverEpoch == epoch) {      // not an epoch change: the link broke, restart from what was heard
    Serial.println("stream: lane lost, resync");
    resyncReq = true;
    for (int i = 0; i < 40 && serverEpoch == epoch; i++) vTaskDelay(pdMS_TO_TICKS(50));
  }
}

void streamTask(void *) {
  for (;;) {
    if (WiFi.status() != WL_CONNECTED || !serverKnown) { vTaskDelay(pdMS_TO_TICKS(500)); continue; }
    uint32_t e = serverEpoch;
    if (e != streamEpoch) {          // new epoch: drop everything that was buffered
      flushReq = true;
      while (flushReq) vTaskDelay(1);
      writeRate = 0;
      streamEpoch = e;
    }
    if (playState == 's') { vTaskDelay(pdMS_TO_TICKS(100)); continue; }
    streamOnce(e);
  }
}

void pollTask(void *) {
  for (;;) {
    vTaskDelay(pdMS_TO_TICKS(250));
    static int fails = 0;
    if (WiFi.status() != WL_CONNECTED || !serverKnown) continue;
    WiFiClient c;
    if (!c.connect(serverIp, serverPort, 1000)) {
      if (++fails >= 12 && cfgServer.isEmpty()) serverKnown = false;   // server moved? look for it again
      continue;
    }
    fails = 0;
    uint32_t bufMs = outRate ? (uint32_t)((wr - rd) * 1000 / outRate) : 0;
    bool rs = resyncReq;
    static uint32_t lastBytes = 0, lastMs = 0;
    uint32_t now = millis(), bytes = rxBytes;
    uint32_t kbps = lastMs && now > lastMs ? (uint32_t)((uint64_t)(bytes - lastBytes) * 8 / (now - lastMs)) : 0;
    lastBytes = bytes; lastMs = now;
    c.printf("GET /esp/poll?epoch=%u&played=%llu&buf=%u&rssi=%d&kbps=%u%s HTTP/1.0\r\nHost: %s\r\n\r\n",
             streamEpoch, (unsigned long long)played, bufMs, WiFi.RSSI(), kbps, rs ? "&resync=1" : "", serverIp);
    if (rs) resyncReq = false;
    String resp;
    uint32_t t0 = millis();
    while (millis() - t0 < 1000 && (c.connected() || c.available())) {
      while (c.available()) resp += (char)c.read();
      vTaskDelay(1);
    }
    c.stop();
    int body = resp.indexOf("\r\n\r\n");
    if (body < 0) continue;
    unsigned e = 0, v = 0;
    char st[8] = {0};
    if (sscanf(resp.c_str() + body + 4, "%u %7s %u", &e, st, &v) == 3) {
      volumePct = v;
      playState = !strcmp(st, "play") ? 'p' : !strcmp(st, "pause") ? 'z' : 's';
      serverEpoch = e;
    }
  }
}

// ---------- finding the server ----------
void discoveryTask(void *) {
  WiFiUDP udp;
  for (;;) {
    vTaskDelay(pdMS_TO_TICKS(1000));
    if (serverKnown || WiFi.status() != WL_CONNECTED) continue;
    if (!cfgServer.isEmpty()) {                       // typed in on the setup page
      IPAddress ip;
      String host = cfgServer;
      int colon = host.indexOf(':');
      if (colon > 0) { serverPort = host.substring(colon + 1).toInt(); host = host.substring(0, colon); }
      if (ip.fromString(host) || WiFi.hostByName(host.c_str(), ip)) {
        strlcpy(serverIp, ip.toString().c_str(), sizeof(serverIp));
        serverKnown = true;
        Serial.printf("server (set by hand): %s:%u\n", serverIp, serverPort);
      }
      continue;
    }
    udp.begin(0);
    udp.beginPacket(WiFi.broadcastIP(), DISCOVERY_PORT);
    udp.print("S3HIFI?");
    udp.endPacket();
    uint32_t t0 = millis();
    while (millis() - t0 < 800) {
      if (udp.parsePacket() > 0) {
        char b[32] = {0};
        udp.read(b, sizeof(b) - 1);
        if (!strncmp(b, "S3HIFI ", 7)) {
          serverPort = atoi(b + 7);
          strlcpy(serverIp, udp.remoteIP().toString().c_str(), sizeof(serverIp));
          serverKnown = true;
          Serial.printf("server found: %s:%u\n", serverIp, serverPort);
          break;
        }
      }
      vTaskDelay(pdMS_TO_TICKS(20));
    }
    udp.stop();
  }
}

// ---------- setup access point and page ----------
String htmlEsc(const String &s) {
  String o;
  for (char ch : s) {
    if (ch == '<') o += "&lt;"; else if (ch == '>') o += "&gt;"; else if (ch == '"') o += "&quot;"; else if (ch == '&') o += "&amp;"; else o += ch;
  }
  return o;
}

WebServer web(80);
DNSServer dns;
bool portalOn = false;
String netOptions;                                    // <option>s from the last scan

// Scanning makes the access point hop channels and drops the phone that is on the setup page,
// so we scan before the point goes up (and again only when asked).
void scanNetworks() {
  int n = WiFi.scanNetworks();
  netOptions = "";
  for (int i = 0; i < n; i++) {
    String ss = WiFi.SSID(i);
    if (ss.isEmpty() || netOptions.indexOf("\"" + htmlEsc(ss) + "\"") >= 0) continue;
    netOptions += "<option value=\"" + htmlEsc(ss) + "\"" + (ss == cfgSsid ? " selected" : "") + ">" + htmlEsc(ss) +
                  " (" + WiFi.RSSI(i) + " dBm)</option>";
  }
  WiFi.scanDelete();
}


const char PAGE_HEAD[] PROGMEM = R"(<!doctype html><html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>S3 Hi-Fi</title><style>
body{margin:0;padding:20px 16px;background:#121214;color:#ecebe8;font:16px/1.4 system-ui,sans-serif}
h1{font-size:20px;margin:0 0 4px}p{color:#8f8d88;margin:0 0 16px}label{display:block;margin:14px 0 6px}
select,input{width:100%;box-sizing:border-box;height:46px;border-radius:10px;border:1px solid #2a2a30;background:#1b1b1f;color:#ecebe8;padding:0 12px;font:inherit}
button{margin-top:20px;width:100%;height:50px;border:0;border-radius:10px;background:#d4a95a;color:#1a1408;font:600 17px system-ui}
small{color:#8f8d88}</style></head><body>)";

void pageRoot() {
  String h = FPSTR(PAGE_HEAD);
  h += "<h1>S3 Hi-Fi — настройка</h1><p>Выберите домашнюю сеть Wi-Fi. Сервер плеер найдёт сам.</p>";
  h += "<form method=post action=/save><label for=ssid>Сеть Wi-Fi</label><select id=ssid name=ssid>";
  h += netOptions;
  h += "<option value=\"\">другая сеть…</option></select>";
  h += "<p style=\"margin:8px 0 0\"><small><a style=\"color:#d4a95a\" href=/rescan>Обновить список</a> — "
       "телефон на пару секунд отключится от точки, потом вернётся</small></p>";
  h += "<label for=other>Имя сети, если её нет в списке</label><input id=other name=other autocomplete=off>";
  h += "<label for=pass>Пароль</label><input id=pass name=pass type=password autocomplete=off>";
  h += "<label for=server>Адрес сервера <small>(можно не заполнять)</small></label><input id=server name=server value=\"" + htmlEsc(cfgServer) + "\" placeholder=\"найти автоматически\">";
  h += "<button>Сохранить и подключиться</button></form>";
  h += "<p style=\"margin-top:24px\"><small>Плеер " + WiFi.macAddress() + ". Чтобы стереть настройки, держите кнопку BOOT 5 секунд.</small></p></body></html>";
  web.send(200, "text/html; charset=utf-8", h);
}

void pageSave() {
  String ss = web.arg("ssid");
  if (ss.isEmpty()) ss = web.arg("other");
  ss.trim();
  if (ss.isEmpty()) { web.sendHeader("Location", "/"); web.send(302); return; }
  Preferences prefs;
  prefs.begin("s3hifi", false);
  prefs.putString("ssid", ss);
  prefs.putString("pass", web.arg("pass"));
  String sv = web.arg("server"); sv.trim();
  prefs.putString("server", sv);
  prefs.end();
  String h = FPSTR(PAGE_HEAD);
  h += "<h1>Сохранено</h1><p>Плеер перезагружается и подключается к сети «" + htmlEsc(ss) + "». "
       "Если пароль неверный, через минуту снова появится сеть «S3 Hi-Fi Setup».</p></body></html>";
  web.send(200, "text/html; charset=utf-8", h);
  delay(1500);
  ESP.restart();
}

void startPortal() {
  if (portalOn) return;
  WiFi.mode(WIFI_STA);
  scanNetworks();                                     // before the access point: see scanNetworks()
  WiFi.mode(cfgSsid.isEmpty() ? WIFI_AP : WIFI_AP_STA);
  WiFi.softAP(AP_NAME);
  delay(100);
  dns.start(53, "*", WiFi.softAPIP());                // every name -> us: the phone shows the page by itself
  web.on("/", HTTP_GET, pageRoot);
  web.on("/save", HTTP_POST, pageSave);
  web.on("/rescan", HTTP_GET, [] {
    web.sendHeader("Location", "/");
    web.send(302);
    delay(100);
    scanNetworks();
  });
  web.onNotFound([] { web.sendHeader("Location", String("http://") + WiFi.softAPIP().toString() + "/"); web.send(302); });
  web.begin();
  portalOn = true;
  Serial.printf("setup access point \"%s\" at %s\n", AP_NAME, WiFi.softAPIP().toString().c_str());
}

void stopPortal() {
  if (!portalOn) return;
  web.stop();
  dns.stop();
  WiFi.softAPdisconnect(true);
  WiFi.mode(WIFI_STA);
  portalOn = false;
  Serial.println("setup access point closed");
}

void setup() {
  Serial.begin(115200);
  pinMode(0, INPUT_PULLUP);                           // BOOT button: hold 5 s to erase Wi-Fi settings
  ring = (int32_t *)ps_malloc(RING_FRAMES * 8);
  if (!ring) { Serial.println("no PSRAM!"); for (;;) delay(1000); }
  i2sInit();
  Preferences prefs;
  prefs.begin("s3hifi", true);
  cfgSsid = prefs.getString("ssid", "");
  cfgPass = prefs.getString("pass", "");
  cfgServer = prefs.getString("server", "");
  prefs.end();
  WiFi.persistent(false);
  WiFi.setSleep(false);                               // power save adds latency and drops throughput
  if (cfgSsid.isEmpty()) {
    startPortal();
  } else {
    WiFi.mode(WIFI_STA);
    WiFi.begin(cfgSsid.c_str(), cfgPass.c_str());
    Serial.printf("WiFi \"%s\"", cfgSsid.c_str());
    for (int i = 0; i < 60 && WiFi.status() != WL_CONNECTED; i++) { delay(500); Serial.print("."); }
    if (WiFi.status() == WL_CONNECTED) Serial.printf(" OK, IP %s, RSSI %d dBm\n", WiFi.localIP().toString().c_str(), WiFi.RSSI());
    else { Serial.println(" failed"); startPortal(); }
  }
  xTaskCreatePinnedToCore(i2sTask, "i2s", 6144, nullptr, configMAX_PRIORITIES - 2, nullptr, 1);
  xTaskCreatePinnedToCore(streamTask, "stream", 8192, nullptr, 5, nullptr, 0);
  xTaskCreatePinnedToCore(pollTask, "poll", 6144, nullptr, 4, nullptr, 0);
  xTaskCreatePinnedToCore(discoveryTask, "discover", 4096, nullptr, 3, nullptr, 0);
}

void loop() {
  static uint32_t t = 0, lastOk = millis(), lastTry = 0, bootDown = 0, okSince = 0;
  bool up = WiFi.status() == WL_CONNECTED;
  if (portalOn) { dns.processNextRequest(); web.handleClient(); }
  if (up) {
    lastOk = millis();
    if (!okSince) okSince = millis();
    if (portalOn && !cfgSsid.isEmpty() && millis() - okSince > 3000) stopPortal();   // the saved network works again
  } else {
    okSince = 0;
    serverKnown = cfgServer.isEmpty() ? false : serverKnown;
    // retry the saved network, but not while a phone is on the setup page (retries make the AP hop channels)
    bool phoneOnSetup = portalOn && WiFi.softAPgetStationNum() > 0;
    if (!cfgSsid.isEmpty() && !phoneOnSetup && millis() - lastTry > 10000) { lastTry = millis(); WiFi.begin(cfgSsid.c_str(), cfgPass.c_str()); }
    if (millis() - lastOk > 60000) startPortal();     // lost for a minute: let the user fix it from a phone
  }
  if (digitalRead(0) == LOW) {                        // BOOT held for 5 s: forget Wi-Fi and server
    if (!bootDown) bootDown = millis();
    if (millis() - bootDown > 5000) {
      Preferences prefs; prefs.begin("s3hifi", false); prefs.clear(); prefs.end();
      Serial.println("settings erased");
      delay(200);
      ESP.restart();
    }
  } else bootDown = 0;
  if (millis() - t > 2000) {
    t = millis();
    Serial.printf("epoch %u %c vol %d | buf %.1f s @ %u Hz | played %llu | RSSI %d | server %s\n", streamEpoch, playState, volumePct,
                  outRate ? (double)(wr - rd) / outRate : 0.0, outRate, (unsigned long long)played, WiFi.RSSI(), serverKnown ? serverIp : "-");
  }
  delay(portalOn ? 2 : 20);
}
