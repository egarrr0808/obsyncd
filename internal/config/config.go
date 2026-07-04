package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	stconfig "github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
	"gopkg.in/yaml.v3"
)

const DefaultFolderID = "obsidian"

type File struct {
	DeviceName  string       `yaml:"device_name"`
	VaultPath   string       `yaml:"vault_path"`
	RemoteNodes []RemoteNode `yaml:"remote_nodes"`
}

type RemoteNode struct {
	Name       string `yaml:"name"`
	DeviceID   string `yaml:"device_id"`
	Address    string `yaml:"address"`
	Introducer bool   `yaml:"introducer"`
}

func Load(path string) (File, error) {
	bs, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var cfg File
	if err := yaml.Unmarshal(bs, &cfg); err != nil {
		return File{}, err
	}
	if err := cfg.Validate(); err != nil {
		return File{}, err
	}
	return cfg, nil
}

func (c File) Validate() error {
	if strings.TrimSpace(c.DeviceName) == "" {
		return errors.New("device_name is required")
	}
	if strings.TrimSpace(c.VaultPath) == "" {
		return errors.New("vault_path is required")
	}
	if !filepath.IsAbs(c.VaultPath) {
		return fmt.Errorf("vault_path must be absolute: %s", c.VaultPath)
	}
	if len(c.RemoteNodes) == 0 {
		return errors.New("remote_nodes requires at least one node")
	}
	for idx, node := range c.RemoteNodes {
		if strings.TrimSpace(node.Name) == "" {
			return fmt.Errorf("remote_nodes[%d].name is required", idx)
		}
		if strings.TrimSpace(node.DeviceID) == "" {
			return fmt.Errorf("remote_nodes[%d].device_id is required", idx)
		}
		if _, err := protocol.DeviceIDFromString(node.DeviceID); err != nil {
			return fmt.Errorf("remote_nodes[%d].device_id: %w", idx, err)
		}
		if strings.TrimSpace(node.Address) == "" {
			return fmt.Errorf("remote_nodes[%d].address is required", idx)
		}
		if node.Address == "dynamic" {
			continue
		}
		u, err := url.Parse(node.Address)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("remote_nodes[%d].address must be dynamic or scheme://host:port", idx)
		}
	}
	return nil
}

func BuildSyncthingConfig(app File, myID protocol.DeviceID, configPath string, evLogger events.Logger) (stconfig.Wrapper, error) {
	cfg := stconfig.New(myID)

	cfg.GUI.Enabled = false
	cfg.GUI.RawAddress = "127.0.0.1:0"
	cfg.Options.GlobalAnnEnabled = false
	cfg.Options.RawGlobalAnnServers = nil
	cfg.Options.RelaysEnabled = false
	cfg.Options.RawListenAddresses = []string{"tcp://0.0.0.0:22000", "quic://0.0.0.0:22000"}
	cfg.Options.NATEnabled = false
	cfg.Options.URAccepted = -1
	cfg.Options.StartBrowser = false

	self := cfg.Defaults.Device.Copy()
	self.DeviceID = myID
	self.Name = app.DeviceName
	self.Addresses = []string{"dynamic"}

	devices := []stconfig.DeviceConfiguration{self}
	folderDevices := []stconfig.FolderDeviceConfiguration{{DeviceID: myID}}
	seen := map[protocol.DeviceID]struct{}{myID: {}}

	for _, node := range app.RemoteNodes {
		id, err := protocol.DeviceIDFromString(node.DeviceID)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("duplicate remote device: %s", id)
		}
		seen[id] = struct{}{}

		dev := cfg.Defaults.Device.Copy()
		dev.DeviceID = id
		dev.Name = node.Name
		dev.Addresses = []string{node.Address}
		dev.Introducer = node.Introducer
		devices = append(devices, dev)
		folderDevices = append(folderDevices, stconfig.FolderDeviceConfiguration{DeviceID: id})
	}
	cfg.Devices = devices

	folder := cfg.Defaults.Folder.Copy()
	folder.ID = DefaultFolderID
	folder.Label = "Obsidian Vault"
	folder.FilesystemType = stconfig.FilesystemTypeBasic
	folder.Path = app.VaultPath
	folder.Type = stconfig.FolderTypeSendReceive
	folder.Devices = folderDevices
	folder.FSWatcherEnabled = true
	folder.RescanIntervalS = 3600
	folder.MaxConflicts = -1
	cfg.Folders = []stconfig.FolderConfiguration{folder}

	return stconfig.Wrap(configPath, cfg, myID, evLogger), nil
}
