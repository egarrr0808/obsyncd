#!/usr/bin/env sh
set -eu

PREFIX="${PREFIX:-$HOME/.local}"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/obsyncd"
CONFIG_FILE="$CONFIG_DIR/config.yaml"
SRC_DIR="${OBSYNCD_SRC_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}"

need_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

need_cmd go

cd "$SRC_DIR"
if [ ! -f go.mod ] || [ ! -d cmd/obsyncd ] || [ ! -d cmd/obsyncctl ]; then
	echo "install.sh must be run from an obsyncd source checkout" >&2
	echo "clone first: git clone https://github.com/egarrr0808/obsyncd.git ~/obsyncd" >&2
	exit 1
fi

go build -tags noassets -o obsyncd ./cmd/obsyncd
go build -tags noassets -o obsyncctl ./cmd/obsyncctl

mkdir -p "$PREFIX/bin"
if [ -w "$PREFIX/bin" ]; then
	install -m 0755 obsyncd "$PREFIX/bin/obsyncd"
	install -m 0755 obsyncctl "$PREFIX/bin/obsyncctl"
else
	sudo install -m 0755 obsyncd "$PREFIX/bin/obsyncd"
	sudo install -m 0755 obsyncctl "$PREFIX/bin/obsyncctl"
fi

mkdir -p "$CONFIG_DIR"
if [ ! -f "$CONFIG_FILE" ]; then
	cat > "$CONFIG_FILE" <<'CFG'
device_name: "CHANGE-ME"
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

cat <<MSG
obsyncd installed.

Next:
  0. Add $PREFIX/bin to PATH if needed
  1. Edit $CONFIG_FILE
  2. Get this machine ID: obsyncd -config $CONFIG_FILE id
  3. Run daemon: obsyncd -config $CONFIG_FILE
  4. Check status: obsyncctl status
MSG
