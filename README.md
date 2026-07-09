# obsyncd

Headless Obsidian Markdown sync daemon prototype built on Syncthing core libraries.

## Install

Clone once, then run the installer from the checkout:

```bash
git clone https://github.com/egarrr0808/obsyncd.git ~/obsyncd
cd ~/obsyncd
./scripts/install.sh
```

The installer does not clone or pull the repo. It only builds the source tree it is already inside.

The installer:

- builds `obsyncd` and `obsyncctl` with `-tags noassets`
- installs both into `~/.local/bin` by default
- creates `~/.config/obsyncd/config.yaml` if missing
- installs and restarts a user service on Linux systemd or macOS launchd

Use `INSTALL_SERVICE=0 ./scripts/install.sh` to only install binaries. Use `RESTART_SERVICE=0 ./scripts/install.sh` to avoid restarting the daemon.

## Config

Server hub config:

```yaml
device_name: "oracle-vps"
role: "hub"
vault_path: "/home/ubuntu/Obsidian/PersonalVault"

remote_nodes:
  - name: "laptop-1"
    device_id: "LAPTOP_1_DEVICE_ID"
    address: "dynamic"
    introducer: false
  - name: "laptop-2"
    device_id: "LAPTOP_2_DEVICE_ID"
    address: "dynamic"
    introducer: false
```

Laptop client config:

```yaml
device_name: "my-laptop"
role: "client"
vault_path: "/home/YOURUSER/Obsidian/PersonalVault"

remote_nodes:
  - name: "oracle-vps"
    device_id: "SERVER_DEVICE_ID"
    address: "tcp://SERVER_PUBLIC_IP:22000"
    introducer: true
```

The hub publishes the accepted vault as a send-only Syncthing folder. Clients receive that vault and send edits through the internal `obsyncd-proposals` folder. If the hub sees a stale edit, it creates a conflict job for the client; resolve it with `obsyncctl` on that laptop.

Clients also run a background checker every second. It hashes Markdown files, submits changed files to the hub automatically, and submits deletes for tracked files that disappeared. This is a fallback for missed filesystem events and daemon restarts.

Get a machine's Syncthing device ID:

```bash
obsyncd -config ~/.config/obsyncd/config.yaml id
```

## Run

```bash
systemctl --user restart obsyncd.service
```

Or run manually if you installed without a service:

```bash
obsyncd -config ~/.config/obsyncd/config.yaml
```

```bash
obsyncctl status
obsyncctl rescan
obsyncctl
```

`obsyncctl` without a command opens the conflict-resolution TUI.

On startup, both `obsyncd` and `obsyncctl` try a best-effort self-update from `~/obsyncd` or `OBSYNCD_SRC_DIR`. If `origin/main` changed, they pull, rebuild both binaries into the current writable install dir or `OBSYNCD_INSTALL_DIR`, then relaunch once. Set `OBSYNCD_NO_UPDATE=1` to disable this.

## Server Firewall

Open Syncthing sync traffic on the Oracle/Ubuntu server:

```bash
sudo iptables -I INPUT -p tcp --dport 22000 -j ACCEPT
sudo iptables -I INPUT -p udp --dport 22000 -j ACCEPT
sudo netfilter-persistent save
```

Keep source port as `any`; `22000` is the destination port.

Do not expose Syncthing GUI port `8384`. obsyncd IPC uses a local Unix socket only.
