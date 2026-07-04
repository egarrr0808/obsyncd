package config

import (
	"os"
	"path/filepath"
	"testing"

	stconfig "github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
	"gopkg.in/yaml.v3"
)

const deviceID = "AIR6LPZ-7K4PTTV-UXQSMUU-CPQ5YWH-OEDFIIQ-JUG777G-2YQXXR5-YD6AWQR"

func TestLoadYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
device_name: "my-laptop"
role: "client"
vault_path: "/home/egarrr/Obsidian/PersonalVault"
remote_nodes:
  - name: "oracle-vps"
    device_id: "`+deviceID+`"
    address: "tcp://your-oracle-vps-ip:22000"
    introducer: true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceName != "my-laptop" || !cfg.RemoteNodes[0].Introducer {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if cfg.NormalizedRole() != "client" {
		t.Fatalf("wrong role: %s", cfg.NormalizedRole())
	}
}

func TestRejectsInvalidDeviceID(t *testing.T) {
	cfg := File{
		DeviceName: "host",
		VaultPath:  "/tmp/vault",
		RemoteNodes: []RemoteNode{{
			Name:     "oracle",
			DeviceID: "ORACLE-XXXXX-XXXXX-XXXXX-XXXXX",
			Address:  "tcp://127.0.0.1:22000",
		}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid device ID error")
	}
}

func TestBuildSyncthingConfigIsHeadless(t *testing.T) {
	myID, err := protocol.DeviceIDFromString("GYRZZQB-IRNPV4Z-T7TC52W-EQYJ3TT-FDQW6MW-DFLMU42-SSSU6EM-FBK2VAY")
	if err != nil {
		t.Fatal(err)
	}
	app := File{
		DeviceName:   "my-laptop",
		Role:         "client",
		VaultPath:    "/tmp/vault",
		ProposalPath: filepath.Join(t.TempDir(), "proposals"),
		RemoteNodes: []RemoteNode{{
			Name:       "oracle-vps",
			DeviceID:   deviceID,
			Address:    "tcp://203.0.113.10:22000",
			Introducer: true,
		}},
	}
	cfg, err := BuildSyncthingConfig(app, myID, filepath.Join(t.TempDir(), "config.xml"), events.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GUI().Enabled {
		t.Fatal("GUI enabled")
	}
	opts := cfg.Options()
	if opts.GlobalAnnEnabled || opts.RelaysEnabled || opts.NATEnabled {
		bs, _ := yaml.Marshal(opts)
		t.Fatalf("network discovery not disabled:\n%s", bs)
	}
	if cfg.FolderList()[0].Path != app.VaultPath {
		t.Fatalf("wrong vault path")
	}
	folders := cfg.FolderList()
	if len(folders) != 2 {
		t.Fatalf("folders = %d", len(folders))
	}
	if folders[0].Type != stconfig.FolderTypeReceiveOnly {
		t.Fatalf("client vault should be receiveonly: %v", folders[0].Type)
	}
	if folders[1].ID != ProposalFolderID || folders[1].Path != app.ProposalPath {
		t.Fatalf("wrong proposal folder: %#v", folders[1])
	}
	var introducer bool
	for _, dev := range cfg.DeviceList() {
		if dev.Name == "oracle-vps" {
			introducer = dev.Introducer
		}
	}
	if !introducer {
		t.Fatal("oracle device is not introducer")
	}
}
