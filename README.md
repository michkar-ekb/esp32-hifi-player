# S3 Hi-Fi

**English** · [Русский](README.ru.md)

A network Hi-Fi player for your own music library (FLAC, WAV, MP3, DSD) and internet radio. The music lives on a home server;
the player, built on an ESP32-S3, receives a ready-to-play stream over Wi-Fi and feeds it
to an ES9038Q2M DAC. You control it from your phone, in the browser.

> **Status:** in daily use at home. Server 0.8 (with a Windows installer), player firmware 0.8
> (the player is also a USB DAC for a PC), case and wiring are done.
> No display on purpose: everything is on the phone, cover art included. Next: more connections
> for listening over mobile internet (in a car).

<p>
  <img src="photos/player_case.jpg" width="49%" alt="Player in its case on the stereo">
  <img src="photos/player_inside.jpg" width="49%" alt="Inside: DAC, DC-DC module and the ESP32-S3 terminal board">
</p>

The bench build: in the case on the stereo, and inside.

![Block diagram](hardware/schematic.svg)

## New in 0.7: the player is also a USB DAC for your PC

Plug the player into a computer with a USB cable and it becomes an ordinary sound card called
**"S3 Hi-Fi"**. No driver needed: Windows 10/11, Linux and macOS support it out of the box (USB Audio Class 2).

> ⚠️ **The board has two USB-C ports, and they are different.** Flash through the **COM** (UART) port.
> Sound from the PC goes through the **USB** port (the ESP32-S3's native USB). **After flashing, move the
> cable to the other port — USB**, or the PC will not see the sound card.

- **Up to 24 bit / 96 kHz:** 16 and 24 bit, 44.1 / 48 / 88.2 / 96 kHz. USB Full Speed allows no more.
- **Asynchronous:** the player's clock sets the pace and the PC follows it (feedback endpoint),
  so the two clocks never drift apart and there are no clicks.
- **Network and USB work at the same time.** When the PC starts playing (and the stream has sound in it),
  network playback pauses and the player's settings page shows "USB-ЦАП: играет компьютер". ▶ in the
  remote takes the player back to the network. 5 s of silence, a closed stream or a pulled cable releases
  USB; the network does not resume by itself — press ▶.
- **Volume** is the Windows slider. For the best sound keep it at 100 % and use the amplifier's knob.
- **To stop Windows resampling everything to 48 kHz**, pick **WASAPI (Exclusive)** output to "S3 Hi-Fi"
  in your player (AIMP: Settings → Playback → Device). The rate then follows each file. Otherwise Windows
  sends the format set in "Speaker properties → Advanced".
- USB diagnostics, if you are curious: `http://<player address>/usb`.

## Quick start

1. **Server** on a Linux PC that holds the music:
   `curl -fsSL https://raw.githubusercontent.com/michkar-ekb/esp32-hifi-player/main/install.sh | sudo sh -s -- /path/to/music`
   **Windows:** the installer `s3hifi-setup-<version>.exe` from [Releases](https://github.com/michkar-ekb/esp32-hifi-player/releases/latest), see
   [Server on Windows](#server-on-windows) below.
2. **Player**: download `s3hifi-flash-windows.zip` from [Releases](https://github.com/michkar-ekb/esp32-hifi-player/releases/latest), unpack, connect the board
   **to the COM port** and run `flash.bat` — it only asks for the COM port number (Linux/Mac: `esptool`, see [firmware](firmware)). Then
   connect a phone to the "S3 Hi-Fi Setup" Wi-Fi and pick your network. The player finds the server by itself.
   **For sound from the PC, move the cable to the other port — USB.** The cable is needed once: from 0.7 on,
   new versions are installed over Wi-Fi (see [firmware](firmware)).
3. **Remote**: open `http://<server>:8097` on the phone.

### Server on Windows

1. Download `s3hifi-setup-<version>.exe` from [Releases](https://github.com/michkar-ekb/esp32-hifi-player/releases/latest) and run it.
2. **Windows may show a blue "Windows protected your PC" window** (or an antivirus warning). This happens
   with any program that has no paid code-signing certificate — it is not a virus. Click "More info" →
   "Run anyway". All the source code is open in this repository, and the file checksums are in
   `SHA256SUMS` next to the release. The same window may appear for `flash.bat`.
3. The installer puts the server into Program Files, adds it to startup and allows it in the firewall
   for private networks. If an antivirus blocks the startup entry, the installer says so.
4. **On the first start the server asks for the music folder.** Any folder works: on this PC, a mapped
   network drive (e.g. `M:`) or `\\computer\share` — the path can be typed into the dialog. A folder on a
   mapped drive is remembered as its network path, so the music is found after a reboot even before
   Windows reconnects the drive letter.
5. The server lives as a record icon in the notification area. **Left or right click opens the menu:**
   "Open remote", "Music: …" (open the folder), "Choose music folder…", "Log", "Exit".
6. Uninstall from Settings → Apps. Settings and the chosen folder stay in `%USERPROFILE%\.s3hifi`.

For DTS, APE, WavPack and ALAC put `ffmpeg.exe` and `ffprobe.exe` into the program folder (any LGPL build).

## How it works

- **Streaming server** (a home PC or a mini PC, Windows or Linux) stores the music, decodes
  FLAC / WAV / MP3 / DSD and MP3 radio to PCM, keeps the play queue and the radio list,
  and serves the web remote.
- **ESP32-S3** decodes nothing: it receives the PCM stream over TCP, keeps a ~10 second
  buffer in PSRAM and plays it out over I2S at a steady rate. The player sets the pace,
  so the server clock and the player clock never drift apart.
- **ES9038Q2M DAC** has its own oscillator (no MCLK needed) and drives a ~2 V line output
  into an amplifier or powered speakers.

Quality ceiling is 24-bit / 96 kHz: the stream is 4.6 Mbit/s, well within what Wi-Fi delivers.

## Parts

| Block | Part | Notes |
|---|---|---|
| Player | ESP32-S3 DevKitC-1, **N16R8** | PSRAM is required for the buffer |
| DAC | ES9038Q2M board (Rod Rain Audio) | I2S input: GND, BCLK, FSCLK, DATA; RCA output |
| Power | 12 V / 2 A adapter + DC-DC step-down module 12 → 5 V | 12 V goes to the DAC, 5 V to the ESP32-S3 |
| Terminal board | ESP32-S3 terminal board (in this repository) | sockets for the devkit + 6 pads for wires |

## Wiring

![Wiring diagram](hardware/wiring.svg)

| ESP32-S3 | Terminal board pad | DAC |
|---|---|---|
| 5V | 5V | — (from the DC-DC module) |
| GND | GND | — (from the DC-DC module) |
| GPIO42 | DATA | DATA |
| GPIO2 | LRCK | FSCLK |
| GPIO1 | BCLK | BCLK |
| GND | GND_A | GND |

Power is split as a **star right at the DAC input jack**: one pair of wires goes to the DAC,
the other to the DC-DC module. The ESP32 current never flows through the DAC's traces.

## ESP32-S3 terminal board

The devkit plugs into sockets, and every signal the player needs comes out to large pads
for soldering wires. Single-sided, 48 × 69.65 mm, all traces on the bottom layer, made for toner transfer
or a marker on a 3D printer.

<img src="hardware/pcb/terminal_board.png" width="360" alt="Terminal board layout">

- `hardware/pcb/terminal_board_gerber.zip` — Gerber files to order or etch the board
- `hardware/pcb/terminal_board_bottom_marker.stl` — bottom layer for marker plotting (mirrored, 0.2 mm)

## Software

- [`server/`](server) — streaming server in Go with the built-in web remote: folders, queue, radio.
  One binary for **Windows and Linux**.
- [`firmware/`](firmware) — player firmware for the ESP32-S3 (Arduino core 3.3.8): network player and USB DAC.

The ES9038Q2M board needs **32-bit I2S samples**; with 16-bit slots it plays noise.
DSD is converted to PCM 24 bit / 88.2 kHz on the server: the board's I2S header cannot carry native DSD.

## Case

3D-printed base plate and lid, `hardware/case/`: the DAC, the DC-DC module and the terminal board
on standoffs; RCA and the headphone jack at the front, 12 V and both USB-C at the rear.
Rounded 3 mm edges, printed without supports. Four M3 × 12 screws hold the lid from below.
This is just one way to house it: any case that fits your modules will do.

## License

Code: MIT. Schematic and board: CC BY-SA 4.0 — build it, change it, just credit the author.
