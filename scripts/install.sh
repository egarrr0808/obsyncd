#!/usr/bin/env sh
set -eu

PREFIX="${PREFIX:-$HOME/.local}"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/obsyncd"
CONFIG_FILE="${OBSYNCD_CONFIG:-$CONFIG_DIR/config.yaml}"
SRC_DIR="${OBSYNCD_SRC_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}"
INSTALL_SERVICE="${INSTALL_SERVICE:-1}"
RESTART_SERVICE="${RESTART_SERVICE:-1}"

need_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		case "$1" in
		go)
			echo "Install Go first, then rerun this script." >&2
			echo "Arch:   sudo pacman -S go" >&2
			echo "Ubuntu: sudo apt update && sudo apt install -y golang-go" >&2
			echo "macOS:  brew install go" >&2
			;;
		esac
		exit 1
	fi
}

abs() {
	(cd "$1" && pwd)
}

need_cmd go

SRC_DIR="$(abs "$SRC_DIR")"
cd "$SRC_DIR"
if [ ! -f go.mod ] || [ ! -d cmd/obsyncd ] || [ ! -d cmd/obsyncctl ]; then
	echo "install.sh must be run from an obsyncd source checkout" >&2
	echo "clone first: git clone https://github.com/egarrr0808/obsyncd.git ~/obsyncd" >&2
	exit 1
fi

mkdir -p "$PREFIX/bin" "$CONFIG_DIR"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM
go build -tags noassets -o "$TMP_DIR/obsyncd" ./cmd/obsyncd
go build -tags noassets -o "$TMP_DIR/obsyncctl" ./cmd/obsyncctl
install -m 0755 "$TMP_DIR/obsyncd" "$PREFIX/bin/obsyncd"
install -m 0755 "$TMP_DIR/obsyncctl" "$PREFIX/bin/obsyncctl"

if [ ! -f "$CONFIG_FILE" ]; then
	cat > "$CONFIG_FILE" <<'CFG'
device_name: "CHANGE-ME"
role: "client"
vault_path: "/home/CHANGE-ME/Obsidian/PersonalVault"

remote_nodes:
  - name: "oracle-vps"
    device_id: "SERVER_DEVICE_ID"
    address: "tcp://SERVER_PUBLIC_IP:22000"
    introducer: true
CFG
	chmod 600 "$CONFIG_FILE"
	echo "created sample config: $CONFIG_FILE"
fi

install_systemd_user() {
	SERVICE_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
	SERVICE_FILE="$SERVICE_DIR/obsyncd.service"
	mkdir -p "$SERVICE_DIR"
	cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=obsyncd user daemon
After=network-online.target

[Service]
Type=simple
Environment=OBSYNCD_SRC_DIR=$SRC_DIR
Environment=OBSYNCD_INSTALL_DIR=$PREFIX/bin
ExecStart=$PREFIX/bin/obsyncd -config $CONFIG_FILE
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
	systemctl --user daemon-reload
	systemctl --user enable obsyncd.service >/dev/null
	if [ "$RESTART_SERVICE" = "1" ]; then
		systemctl --user restart obsyncd.service
	fi
	echo "installed systemd user service: $SERVICE_FILE"
}

install_launchd_user() {
	PLIST_DIR="$HOME/Library/LaunchAgents"
	PLIST_FILE="$PLIST_DIR/com.obsyncd.plist"
	mkdir -p "$PLIST_DIR"
	cat > "$PLIST_FILE" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.obsyncd</string>
  <key>ProgramArguments</key>
  <array>
    <string>$PREFIX/bin/obsyncd</string>
    <string>-config</string>
    <string>$CONFIG_FILE</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>OBSYNCD_SRC_DIR</key><string>$SRC_DIR</string>
    <key>OBSYNCD_INSTALL_DIR</key><string>$PREFIX/bin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
EOF
	if [ "$RESTART_SERVICE" = "1" ]; then
		launchctl bootout "gui/$(id -u)" "$PLIST_FILE" >/dev/null 2>&1 || true
		launchctl bootstrap "gui/$(id -u)" "$PLIST_FILE"
	fi
	echo "installed launchd user agent: $PLIST_FILE"
}

if [ "$INSTALL_SERVICE" = "1" ]; then
	case "$(uname -s)" in
	Linux)
		if command -v systemctl >/dev/null 2>&1; then
			install_systemd_user
		else
			echo "systemctl not found; run daemon manually: $PREFIX/bin/obsyncd -config $CONFIG_FILE"
		fi
		;;
	Darwin)
		install_launchd_user
		;;
	*)
		echo "unsupported service manager; run daemon manually: $PREFIX/bin/obsyncd -config $CONFIG_FILE"
		;;
	esac
fi

cat <<MSG
obsyncd installed.

Binaries:
  $PREFIX/bin/obsyncd
  $PREFIX/bin/obsyncctl

Config:
  $CONFIG_FILE

Next:
  1. Edit config if needed.
  2. Get device ID: $PREFIX/bin/obsyncd -config $CONFIG_FILE id
  3. Check daemon: $PREFIX/bin/obsyncctl status
  4. Resolve conflicts: $PREFIX/bin/obsyncctl

If command not found, add to PATH:
  export PATH="$PREFIX/bin:\$PATH"
MSG
