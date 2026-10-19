// USB DAC mode: the S3's native USB port is a USB Audio Class 2 speaker (no drivers on Windows 10/11,
// Linux, macOS). Full speed allows up to 24 bit / 96 kHz. Asynchronous: our I2S clock sets the pace and
// a feedback endpoint tells the host to send a little more or less.
#pragma once
#include <stdint.h>
#include <WString.h>

void usbAudioBegin();                   // before the network: the PC can play right away

// The stream from the PC, already as 32-bit stereo frames (what the I2S bus takes)
uint32_t usbAvail();                    // frames waiting (I2S task only: it also applies a pending flush)
uint32_t usbLevel();                    // the same, read-only, for the log
void usbRead(int32_t *dst, uint32_t frames);
void usbSkip(uint32_t frames);
uint32_t usbTargetFrames();             // how much we keep buffered (~25 ms)

extern volatile uint32_t usbRate;       // sample rate the PC chose
extern volatile uint8_t usbBits;        // 16 or 24
extern volatile bool usbStreaming;      // the PC opened the stream (alternate setting > 0)
extern volatile uint32_t usbSoundMs;    // millis() of the last packet that was not silence
extern volatile float usbGain;          // the volume slider in Windows (with mute)
extern volatile uint32_t usbDropped;    // frames thrown away / packets lost, for the log
bool usbConnected();                    // plugged into a PC that is awake
String usbDiag();                       // counters and USB registers, for /usb
void usbReplug();                       // drop off the bus and come back: the PC enumerates again
