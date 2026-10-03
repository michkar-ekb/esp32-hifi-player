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
# Environment: PREFIX (default /opt/s3hifi), PORT (8097), NO_SERVICE=1 (only download and check).
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

URL="https://github.com/$REPO/releases/latest/download/s3hifi-linux-$ARCH"
say "Downloading $URL"
mkdir -p "$PREFIX/data"
if command -v curl >/dev/null 2>&1; then
  curl -fL --progress-bar "$URL" -o "$PREFIX/s3hifi.new"
elif command -v wget >/dev/null 2>&1; then
  wget -q --show-progress -O "$PREFIX/s3hifi.new" "$URL"
else
  die "need curl or wget"
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

command -v ffmpeg >/dev/null 2>&1 || say "Optional: install ffmpeg for DTS, APE, M4A, OGG and files above 96 kHz"

IP=$(hostname -I 2>/dev/null | awk '{print $1}')
sleep 1
if systemctl is-active --quiet s3hifi; then
  say ""
  say "S3 Hi-Fi server is running. Music: $MUSIC"
  say "Open the remote on your phone: http://${IP:-<this-computer>}:$PORT"
else
  die "the service did not start; see: journalctl -u s3hifi"
fi
