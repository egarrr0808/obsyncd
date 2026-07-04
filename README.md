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
- installs both into `/usr/local/bin`
- creates `~/.config/obsyncd/config.yaml` if missing

## Config

Example laptop config:

```yaml
device_name: "my-laptop"
vault_path: "/home/YOURUSER/Obsidian/PersonalVault"

remote_nodes:
  - name: "oracle-vps"
    device_id: "SERVER_DEVICE_ID"
    address: "tcp://SERVER_PUBLIC_IP:22000"
    introducer: true
```

Get a machine's Syncthing device ID:

```bash
obsyncd -config ~/.config/obsyncd/config.yaml id
```

## Run

```bash
obsyncd -config ~/.config/obsyncd/config.yaml
```

In another terminal:

```bash
obsyncctl status
obsyncctl rescan
obsyncctl
```

`obsyncctl` without a command opens the conflict-resolution TUI.

## Server Firewall

Open Syncthing sync traffic on the Oracle/Ubuntu server:

```bash
sudo iptables -I INPUT -p tcp --dport 22000 -j ACCEPT
sudo iptables -I INPUT -p udp --dport 22000 -j ACCEPT
sudo netfilter-persistent save
```

Keep source port as `any`; `22000` is the destination port.

Do not expose Syncthing GUI port `8384`. obsyncd IPC uses a local Unix socket only.
