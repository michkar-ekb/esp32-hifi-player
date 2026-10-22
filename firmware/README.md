# ESP32-S3 firmware

**English** · [Русский](README.ru.md)

> ⚠️ **Two USB-C ports, two jobs.** Flash through the **COM** (UART) port. Listen from a PC through the
> **USB** port. After flashing, **move the cable to the USB port** — otherwise the PC sees no sound card.

`player_fw/` — the player, version 0.8: a network player and a USB DAC for a PC. Over the network it decodes nothing: it receives PCM from the
[server](../server), keeps up to ~40 s of CD audio (~12 s of 24/96) in PSRAM, stored as it came (16 or 24 bit) and plays it out over I2S.

- I2S pins: **BCLK = GPIO1, LRCK = GPIO2, DATA = GPIO42**, no MCLK.
- The ES9038Q2M board needs **32-bit I2S samples**: with 16-bit slots it only produces noise.
  16- and 24-bit audio is placed in the upper bits of 32-bit samples.
- The stream comes over **4 TCP connections**: the core's 5.7 KB TCP window limits one connection
  to ~5 Mbit/s, not enough for DSD or 24/96 on a typical home Wi-Fi.
- The network is read through plain lwIP sockets with 8 KB reads: `WiFiClient` copies everything
  through a 1.4 KB buffer, and at 4+ Mbit/s that overhead made the player fall behind.
  Receive rate is reported to the server and shown in the remote (5–8 Mbit/s on a good link).
- Three tasks: network stream and server poll on core 0, I2S output on core 1 with high priority.
  Pause and volume act immediately; a new track or a seek drops the buffer, and playback starts
  again once 2 s of sound have arrived (so a slow link does not stutter right after a skip).

## USB DAC (`usb_audio.cpp`)

The ESP32-S3's native USB port is a **USB Audio Class 2 speaker**: no drivers on Windows 10/11, Linux or macOS.
16 and 24 bit, 44.1 / 48 / 88.2 / 96 kHz — 24/96 is the ceiling of USB Full Speed.

- Asynchronous with an explicit feedback endpoint: the I2S clock sets the pace, the PC sends a little
  more or less to keep ~25 ms in the buffer.
- The PC starts playing with sound in the stream → the PC takes the player and the network pauses (the
  server is told). ▶ in the remote gives it back to the network. 5 s of silence, a closed stream or a
  pulled cable releases USB. The Windows volume slider works (keep it at 100 % for the best sound).
- Windows shared mode resamples to the device format (48 kHz by default); for the file's own rate use
  WASAPI Exclusive in the player app.
- It is a small UAC2 driver of its own on top of TinyUSB: the core's built-in audio class has no feedback
  endpoint. TinyUSB sizes the RX FIFO for two 582-byte packets, more than the S3 has, so the driver
  sizes it itself for one. The core 2.0.17 USB driver could not receive isochronous OUT — hence core 3.
- `http://<player>/usb` shows counters, the FIFO setup and the last control requests.

## Flash a ready build

> **Use the COM (UART) port for flashing.** When it is done, move the cable to the **USB** port to listen from the PC.
>
> Flashing the full image by cable **resets the Wi-Fi settings**: set the player up again through
> "S3 Hi-Fi Setup" (see below). You only need the cable once — from 0.7 on, updates go over Wi-Fi.

**Windows, the easy way:** download `s3hifi-flash-windows.zip` from
[Releases](https://github.com/michkar-ekb/esp32-hifi-player/releases/latest), unpack it, connect the board
and run `flash.bat`. It lists the COM ports and asks for one (Enter if there is only one); `esptool.exe`
(official Espressif build) and the firmware are inside.

**Any system:** download `s3hifi-player-esp32s3-n16r8.bin` from
[Releases](https://github.com/michkar-ekb/esp32-hifi-player/releases/latest): bootloader, partitions
and firmware in one image, written at address 0. Connect the devkit by its COM port and run:

```
esptool.py --chip esp32s3 write_flash 0x0 s3hifi-player-esp32s3-n16r8.bin
```

(`pip install esptool`.) The image is built
for ESP32-S3 N16R8 (16 MB flash, 8 MB octal PSRAM).

## Update over Wi-Fi (from 0.7 on)

Firmware 0.7 and later can take a new version over the network, no cable, settings kept. Download
`s3hifi-player-ota.bin` from [Releases](https://github.com/michkar-ekb/esp32-hifi-player/releases/latest) and send it
to the player (`curl` is built into Windows 10/11, Linux and macOS):

```
curl -F "f=@s3hifi-player-ota.bin" http://<player address>/update
```

The player answers `{"ok":true}` and restarts. Its address is on the player's setup page (the remote links
to it as "player settings" / "настройки плеера") or in your router. Versions before 0.7 have no `/update`: flash 0.7 by cable once.

## First start

No Wi-Fi settings are compiled in. On first start (or when the saved network is gone for a minute)
the player opens the access point **"S3 Hi-Fi Setup"**; connect a phone to it and the setup page opens
by itself (or go to `192.168.4.1`). Pick your network, type the password, save. The server is found
automatically: the player broadcasts `S3HIFI?` on UDP port 8097 and the server answers. A server
address can still be typed in on the setup page (`host` or `host:port`), for example a server on a VPS
on the internet. Holding **BOOT** for 5 seconds erases the settings.

The same setup page also opens at the player's own address in the home network (the remote links to
it as "настройки плеера"): it shows the network, signal, server and the USB DAC state, and lets you change them. A blank
password keeps the saved one.

## Build

Arduino core for ESP32 **3.3.8**, board settings (USB mode: USB-OTG / TinyUSB):

```
arduino-cli compile --fqbn esp32:esp32:esp32s3:PSRAM=opi,FlashSize=16M,USBMode=default player_fw
```

Nothing to edit before building: Wi-Fi is set up from a phone (see above).
