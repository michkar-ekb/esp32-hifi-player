# Streaming server

A single program written in Go. It keeps the music library and the play queue, decodes
everything to PCM and streams it to the player. The web remote for the phone is built in.

- **Formats:** FLAC, WAV (16/24 bit), MP3, DSD (DSF / DFF, DSD64–DSD256) and MP3 internet radio
  are decoded by the server itself.
- **With ffmpeg installed** (optional; next to the server or in PATH) it also plays M4A / ALAC, AAC,
  OGG, Opus, APE, WavPack, AIFF and WMA, and "DTS-WAV" discs: a WAV that actually holds a DTS 5.1
  stream. The server spots DTS / AC-3 inside a WAV and never plays it as PCM (that is loud noise);
  ffmpeg decodes it and downmixes to stereo.
- **DSD** is converted to PCM 24 bit / 88.2 kHz with a linear-phase FIR (Kaiser window),
  because the DAC board's I2S header cannot carry native DSD.
- **Bit-perfect** for PCM files: the stream is identical to what ffmpeg decodes;
  seeking is sample-accurate and track changes are gapless.
- **SACD disc images** (`.iso`): the stereo area is shown as songs with titles from the disc and played
  through the same DSD filter. DST-compressed discs are not supported yet.
- **CUE sheets:** an album ripped as one big file (APE / FLAC / WAV) plus a `.cue` is shown as separate
  songs; each plays from its start to the next one, gapless. Windows-1251 cue files (common in Russian
  rips) are read correctly, and a cue that says `album.wav` next to `album.ape` still finds the file.
- **Fast seeking without seek tables:** FLAC and WavPack files are bisected for the right frame/block,
  so a song in the middle of an 80-minute album file starts in a second instead of reading the whole file.
- **Above 96 kHz the server resamples** (SoX resampler via ffmpeg): 176.4/352.8 kHz → 88.2 kHz,
  192/384 kHz → 96 kHz, still 24 bit. 24/192 would be 9.2 Mbit/s — more than the player gets over Wi-Fi.
- **Network shares are fine:** files are read 32 MB ahead in the background, so a share that
  stalls for a few seconds is not heard.
- **One binary, no dependencies.** Builds for Windows and Linux (x86-64 and ARM).
  So far it has been run on Linux only.

## Install

### Linux — one command

```bash
curl -fsSL https://raw.githubusercontent.com/michkar-ekb/esp32-hifi-player/main/install.sh | sudo sh -s -- /path/to/music
```

The script downloads the latest release for your CPU (x86-64, ARM64 such as Raspberry Pi 4/5,
ARMv7 such as Orange Pi / older Raspberry Pi), installs it to `/opt/s3hifi`, creates the `s3hifi`
service that starts with the system and runs as your user, and opens port **8097** (TCP for the remote
and the stream, UDP for player discovery) in firewalld or ufw. Without a folder argument it uses
`~/Music`. Run the same command again to update.

Optional: `sudo dnf install ffmpeg` / `sudo apt install ffmpeg` — for DTS, APE, M4A, OGG,
WavPack and files above 96 kHz.

Then open `http://<this-computer>:8097` on the phone.

### Linux — by hand

1. Download `s3hifi-linux-amd64` (or `-arm64`, `-armv7`) from
   [Releases](https://github.com/michkar-ekb/esp32-hifi-player/releases/latest), save it as
   `/opt/s3hifi/s3hifi`, `chmod +x` it.
2. Try it: `/opt/s3hifi/s3hifi -music ~/Music -data /opt/s3hifi/data`.
3. Start with the system — `/etc/systemd/system/s3hifi.service`:

   ```ini
   [Unit]
   Description=S3 Hi-Fi streaming server
   Wants=network-online.target
   After=network-online.target remote-fs.target

   [Service]
   User=your-user
   ExecStart=/opt/s3hifi/s3hifi -music /home/your-user/Music -data /opt/s3hifi/data -listen :8097
   Restart=always
   RestartSec=5

   [Install]
   WantedBy=multi-user.target
   ```

   `sudo systemctl enable --now s3hifi`
4. Open port 8097 TCP and UDP: `sudo firewall-cmd --permanent --add-port=8097/tcp --add-port=8097/udp && sudo firewall-cmd --reload`
   (Ubuntu: `sudo ufw allow 8097`).

If the music folder is a network share that is not mounted yet, the server still starts:
radio works and the folders appear once the share is up.

### Windows

No installer yet. Download `s3hifi-windows-amd64.exe` from
[Releases](https://github.com/michkar-ekb/esp32-hifi-player/releases/latest), put it in a folder and run:

```
s3hifi-windows-amd64.exe -music "D:\Music"
```

Allow it in the Windows firewall when asked (private networks). For DTS, APE and other extra formats put
`ffmpeg.exe` and `ffprobe.exe` next to it (any LGPL build). Windows has been tested less than Linux.

### Build from source

```bash
cd server
go build -o s3hifi .
```

### Options

| Flag | Default | |
|---|---|---|
| `-music` | `~/Music` | music folder |
| `-data` | `~/.s3hifi` | queue, volume, radio list, cover cache |
| `-listen` | `:8097` | address of the web remote and the player stream |
| `-version` | | print the version |

Folders are browsed as they are on disk; a `cover.jpg` / `folder.jpg` (or any image) in an album
folder is used as the cover.

## Player protocol

The player (ESP32-S3) talks plain HTTP/1.0:

- `GET /esp/poll?epoch=E&played=N&buf=ms&rssi=dBm` — about 4 times a second. The answer is
  `epoch state volume`, for example `5 play 70`. `played` is the number of frames the player
  has output in this epoch; the server uses it to show what is really heard.
- `GET /esp/stream?epoch=E&lane=L&lanes=N` — the PCM stream of the epoch, spread over **N parallel
  connections** (the player uses 4). Chunk *n* goes to lane *n mod N*, the player reads the lanes in the
  same order. Chunk: `"S3P2"` · rate `u32` · bits `u8` (16/24) · channels `u8` · flags `u8` (1 = new track) ·
  `0` · length `u32` · sequence `u32` · PCM data (little-endian, ≤ 4 KB).

  Why lanes: the Arduino ESP32 core has a fixed 5.7 KB TCP window, so one connection tops out at
  ~5 Mbit/s on a Wi-Fi link with 8 ms round trip. DSD converted to 88.2 kHz / 24 bit needs 4.2 Mbit/s,
  24/96 FLAC 4.6. Four lanes give several times that.
- If a lane breaks, the player polls with `&resync=1` and the server restarts the epoch from the
  position that has actually been heard.

Every change of what is heard (new track, seek, stop) starts a new **epoch**: the player drops
its buffer and reconnects. Pause and volume do not change the epoch and act on the player at once.
The player sets the pace: it reads from the network only as fast as it plays, so the clocks never drift.
