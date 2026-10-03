# ESP32-S3 firmware

`player_fw/` — the player, version 0.4. It decodes nothing: it receives PCM from the
[server](../server), keeps about 10 seconds in PSRAM and plays it out over I2S.

- I2S pins: **BCLK = GPIO1, LRCK = GPIO2, DATA = GPIO42**, no MCLK.
- The ES9038Q2M board needs **32-bit I2S samples**: with 16-bit slots it only produces noise.
  16- and 24-bit audio is placed in the upper bits of 32-bit samples.
- The stream comes over **4 TCP connections**: the core's 5.7 KB TCP window limits one connection
  to ~5 Mbit/s, not enough for DSD or 24/96 on a typical home Wi-Fi.
- The network is read through plain lwIP sockets with 8 KB reads: `WiFiClient` copies everything
  through a 1.4 KB buffer, and at 4+ Mbit/s that overhead made the player fall behind.
  Receive rate is reported to the server and shown in the remote (5–8 Mbit/s on a good link).
- Three tasks: network stream and server poll on core 0, I2S output on core 1 with high priority.
  Pause and volume act immediately; a new track or a seek drops the buffer (about 0.5 s to restart).

## First start

No Wi-Fi settings are compiled in. On first start (or when the saved network is gone for a minute)
the player opens the access point **"S3 Hi-Fi Setup"**; connect a phone to it and the setup page opens
by itself (or go to `192.168.4.1`). Pick your network, type the password, save. The server is found
automatically: the player broadcasts `S3HIFI?` on UDP port 8097 and the server answers. A server
address can still be typed in on the setup page. Holding **BOOT** for 5 seconds erases the settings.

## Build

Arduino core for ESP32 **2.0.17**, board settings:

```
arduino-cli compile --fqbn esp32:esp32:esp32s3:PSRAM=opi,FlashSize=16M player_fw
```

Nothing to edit before building: Wi-Fi is set up from a phone (see above).
