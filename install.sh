#!/bin/sh
# S3 Hi-Fi server installer for Linux with systemd.
#
#   curl -fsSL https://raw.githubusercontent.com/michkar-ekb/esp32-hifi-player/main/install.sh | sudo sh -s -- /path/to/music
#
# Downloads the latest release for this CPU (x86-64, ARM64, ARMv7), installs it to /opt/s3hifi,
# creates the "s3hifi" service that starts with the system, and opens port 8097 (TCP for the remote
# and the stream, UDP for player discovery). Run it again to update. Without a music folder argument
# ~/Music of the user who ran sudo is used.
#
# If ffmpeg is missing, it asks whether to install it from the system packages (needed for files above
# 96 kHz, DTS, APE, M4A, OGG).
#
# Environment: PREFIX (default /opt/s3hifi), PORT (8097), NO_SERVICE=1 (only download and check),
# FFMPEG=yes / FFMPEG=no (answer the ffmpeg question without asking).
set -eu

REPO=michkar-ekb/esp32-hifi-player
PREFIX=${PREFIX:-/opt/s3hifi}
PORT=${PORT:-8097}
MUSIC=${1:-}

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run with sudo (it installs a system service)"

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  armv7* | armhf) ARCH=armv7 ;;
  *) die "this CPU is not supported yet: $(uname -m)" ;;
esac

RUN_USER=${SUDO_USER:-root}
if [ -z "$MUSIC" ]; then
  HOME_DIR=$(getent passwd "$RUN_USER" | cut -d: -f6)
  MUSIC="$HOME_DIR/Music"
fi
[ -d "$MUSIC" ] || say "note: music folder $MUSIC does not exist yet; the server will show it once it does"

BASE="https://github.com/$REPO/releases/latest/download"
fetch() {  # fetch URL FILE [quiet]
  if command -v curl >/dev/null 2>&1; then
    if [ -n "${3:-}" ]; then curl -fsL "$1" -o "$2"; else curl -fL --progress-bar "$1" -o "$2"; fi
  elif command -v wget >/dev/null 2>&1; then
    if [ -n "${3:-}" ]; then wget -q -O "$2" "$1"; else wget -q --show-progress -O "$2" "$1"; fi
  else
    die "need curl or wget"
  fi
}
say "Downloading $BASE/s3hifi-linux-$ARCH"
mkdir -p "$PREFIX/data"
fetch "$BASE/s3hifi-linux-$ARCH" "$PREFIX/s3hifi.new"
# the release lists the checksum of every file: the download must match it
if fetch "$BASE/SHA256SUMS" "$PREFIX/SHA256SUMS" quiet && command -v sha256sum >/dev/null 2>&1; then
  want=$(awk -v f="s3hifi-linux-$ARCH" '$2 == f || $2 == "*"f {print $1}' "$PREFIX/SHA256SUMS")
  got=$(sha256sum "$PREFIX/s3hifi.new" | awk '{print $1}')
  rm -f "$PREFIX/SHA256SUMS"
  [ -n "$want" ] && [ "$want" = "$got" ] || { rm -f "$PREFIX/s3hifi.new"; die "checksum does not match the release, the download is damaged; run the installer again"; }
  say "Checksum OK"
else
  say "note: could not check the checksum (no SHA256SUMS or sha256sum)"
fi
chmod 755 "$PREFIX/s3hifi.new"
"$PREFIX/s3hifi.new" -version >/dev/null 2>&1 || die "the downloaded file does not run on this system"
mv -f "$PREFIX/s3hifi.new" "$PREFIX/s3hifi"
say "Installed: $("$PREFIX/s3hifi" -version)"

if [ "${NO_SERVICE:-}" = 1 ]; then
  say "NO_SERVICE=1: service and firewall left alone"
  exit 0
fi
command -v systemctl >/dev/null 2>&1 || die "systemd not found; start it by hand: $PREFIX/s3hifi -music \"$MUSIC\""

if [ "$RUN_USER" != root ]; then
  chown -R "$RUN_USER" "$PREFIX/data"
fi
cat >/etc/systemd/system/s3hifi.service <<EOF
[Unit]
Description=S3 Hi-Fi streaming server
Wants=network-online.target
After=network-online.target remote-fs.target

[Service]
User=$RUN_USER
ExecStart=$PREFIX/s3hifi -music "$MUSIC" -data $PREFIX/data -listen :$PORT
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable s3hifi >/dev/null 2>&1
systemctl restart s3hifi

if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
  firewall-cmd --quiet --permanent --add-port="$PORT/tcp" --add-port="$PORT/udp" && firewall-cmd --quiet --reload
  say "Firewall (firewalld): port $PORT opened"
elif command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
  ufw allow "$PORT" >/dev/null
  say "Firewall (ufw): port $PORT opened"
fi

# ffmpeg: needed for files above 96 kHz (the server converts them to 96 / 88.2 kHz, the player's Wi-Fi
# cannot carry more) and for DTS, APE, M4A, OGG. Asked only when it is missing.
if ! command -v ffmpeg >/dev/null 2>&1; then
  ans=${FFMPEG:-}
  if [ -z "$ans" ] && [ -r /dev/tty ] && { : </dev/tty; } 2>/dev/null; then
    say ""
    say "ffmpeg is not installed. It is needed for Hi-Res files above 96 kHz (24/192, 24/176.4):"
    say "the server converts them to 96 / 88.2 kHz, otherwise the player's Wi-Fi cannot keep up and"
    say "the sound breaks up. It also plays DTS, APE, M4A and OGG. FLAC, WAV, MP3 and DSD up to 96 kHz"
    say "play without it."
    printf 'Install ffmpeg now? [Y/n] '
    read -r ans </dev/tty || ans=n
  fi
  case "${ans:-y}" in
    [Nn]*) say "ffmpeg skipped; install it later and restart: systemctl restart s3hifi" ;;
    *)
      say "Installing ffmpeg..."
      if command -v apt-get >/dev/null 2>&1; then apt-get install -y ffmpeg >/dev/null 2>&1 || { apt-get update >/dev/null 2>&1 && apt-get install -y ffmpeg >/dev/null 2>&1; }
      elif command -v dnf >/dev/null 2>&1; then dnf install -y ffmpeg >/dev/null 2>&1 || dnf install -y ffmpeg-free >/dev/null 2>&1
      elif command -v pacman >/dev/null 2>&1; then pacman -S --noconfirm --needed ffmpeg >/dev/null 2>&1
      elif command -v zypper >/dev/null 2>&1; then zypper --non-interactive install ffmpeg >/dev/null 2>&1
      elif command -v apk >/dev/null 2>&1; then apk add ffmpeg >/dev/null 2>&1
      fi
      if command -v ffmpeg >/dev/null 2>&1; then
        say "ffmpeg installed"
        systemctl restart s3hifi
      else
        say "could not install ffmpeg automatically; install the ffmpeg package by hand and restart: systemctl restart s3hifi"
      fi
      ;;
  esac
fi

IP=$(hostname -I 2>/dev/null | awk '{print $1}')
sleep 1
if systemctl is-active --quiet s3hifi; then
  say ""
  say "S3 Hi-Fi server is running. Music: $MUSIC"
  say "Open the remote on your phone: http://${IP:-<this-computer>}:$PORT"
else
  die "the service did not start; see: journalctl -u s3hifi"
fi
