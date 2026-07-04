package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appconfig "obsyncd/internal/config"
	eventloop "obsyncd/internal/events"
	"obsyncd/internal/guard"
	"obsyncd/internal/interceptor"
	"obsyncd/internal/ipc"
	"obsyncd/internal/statestore"

	stconfig "github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/db/backend"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/locations"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/svcutil"
	"github.com/syncthing/syncthing/lib/syncthing"
)

type Daemon struct {
	App      *syncthing.App
	Events   events.Logger
	IPC      *ipc.Server
	Socket   string
	FolderID string
}

type Paths struct {
	ConfigFile string
	StateDir   string
}

type appController struct {
	app *syncthing.App
	cfg stconfig.Wrapper
}

func (c appController) Pause(_ context.Context, folder string) error {
	return c.setPaused(folder, true)
}

func (c appController) Resume(_ context.Context, folder string) error {
	return c.setPaused(folder, false)
}

func (c appController) setPaused(folder string, paused bool) error {
	waiter, err := c.cfg.Modify(func(cfg *stconfig.Configuration) {
		fcfg, _, ok := cfg.Folder(folder)
		if !ok || fcfg.Paused == paused {
			return
		}
		fcfg.Paused = paused
		cfg.SetFolder(fcfg)
	})
	if err != nil {
		return err
	}
	waiter.Wait()
	return c.cfg.Save()
}

func (c appController) Rescan(_ context.Context, folder string, paths []string) error {
	return c.app.Internals.ScanFolderSubdirs(folder, paths)
}

func DeviceID(configFile string) (string, error) {
	paths, err := resolvePaths(configFile)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		return "", err
	}
	if err := locations.SetBaseDir(locations.ConfigBaseDir, paths.StateDir); err != nil {
		return "", err
	}
	if err := locations.SetBaseDir(locations.DataBaseDir, paths.StateDir); err != nil {
		return "", err
	}
	cert, err := syncthing.LoadOrGenerateCertificate(locations.Get(locations.CertFile), locations.Get(locations.KeyFile))
	if err != nil {
		return "", fmt.Errorf("certificate: %w", err)
	}
	return syncthingDeviceID(cert).String(), nil
}

func Start(ctx context.Context, configFile string) (*Daemon, error) {
	appCfg, err := appconfig.Load(configFile)
	if err != nil {
		return nil, err
	}
	paths, err := resolvePaths(configFile)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		return nil, err
	}
	if err := ensureSyncIgnores(appCfg.VaultPath); err != nil {
		return nil, err
	}
	if err := locations.SetBaseDir(locations.ConfigBaseDir, paths.StateDir); err != nil {
		return nil, err
	}
	if err := locations.SetBaseDir(locations.DataBaseDir, paths.StateDir); err != nil {
		return nil, err
	}

	evLogger := events.NewLogger()
	loggerCtx, loggerCancel := context.WithCancel(ctx)
	go func() {
		_ = evLogger.Serve(loggerCtx)
	}()
	cert, err := syncthing.LoadOrGenerateCertificate(locations.Get(locations.CertFile), locations.Get(locations.KeyFile))
	if err != nil {
		loggerCancel()
		return nil, fmt.Errorf("certificate: %w", err)
	}
	myID := syncthingDeviceID(cert)

	stCfg, err := appconfig.BuildSyncthingConfig(appCfg, myID, locations.Get(locations.ConfigFile), evLogger)
	if err != nil {
		loggerCancel()
		return nil, err
	}
	if err := stCfg.Save(); err != nil {
		loggerCancel()
		return nil, fmt.Errorf("save syncthing config: %w", err)
	}
	cfgCtx, cfgCancel := context.WithCancel(ctx)
	go func() {
		_ = stCfg.Serve(cfgCtx)
	}()

	db, err := backend.Open(locations.Get(locations.Database), backend.TuningSmall)
	if err != nil {
		cfgCancel()
		loggerCancel()
		return nil, fmt.Errorf("open syncthing db: %w", err)
	}
	app, err := syncthing.New(stCfg, db, evLogger, cert, syncthing.Options{
		NoUpgrade: true,
	})
	if err != nil {
		_ = db.Close()
		cfgCancel()
		loggerCancel()
		return nil, err
	}
	if err := app.Start(); err != nil {
		cfgCancel()
		loggerCancel()
		return nil, err
	}
	oracleID, oracleName, err := oracleDevice(appCfg)
	if err != nil {
		app.Stop(svcutil.ExitError)
		cfgCancel()
		loggerCancel()
		return nil, err
	}
	controller := appController{app: app, cfg: stCfg}
	store := statestore.New(appCfg.VaultPath)
	conflictGuard := &guard.Guard{
		Root:       appCfg.VaultPath,
		StateDir:   paths.StateDir,
		Folder:     appconfig.DefaultFolderID,
		Logger:     evLogger,
		Controller: controller,
		Stager:     store,
	}
	go func() {
		if err := conflictGuard.Run(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	go func() {
		if err := conflictGuard.RunSnapshotScanner(ctx, 250*time.Millisecond); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	go func() {
		if err := (eventloop.BaseCapture{
			Logger: evLogger,
			Folder: appconfig.DefaultFolderID,
			Root:   appCfg.VaultPath,
			Store:  store,
		}).Run(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	conflictInterceptor := &interceptor.Interceptor{
		Root:       appCfg.VaultPath,
		Folder:     appconfig.DefaultFolderID,
		Controller: controller,
		Bases:      store,
	}
	go func() {
		if err := (eventloop.Loop{
			Logger:  evLogger,
			Folder:  appconfig.DefaultFolderID,
			Handler: conflictInterceptor,
		}).Run(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()

	rpcServer, err := ipc.Start(ctx, "", app, stCfg, appconfig.DefaultFolderID, appCfg.VaultPath, store, oracleID, oracleName)
	if err != nil {
		app.Stop(svcutil.ExitError)
		cfgCancel()
		loggerCancel()
		return nil, err
	}
	d := &Daemon{
		App:      app,
		Events:   evLogger,
		IPC:      rpcServer,
		Socket:   rpcServer.Socket(),
		FolderID: appconfig.DefaultFolderID,
	}
	go func() {
		<-ctx.Done()
		app.Stop(svcutil.ExitSuccess)
		cfgCancel()
		loggerCancel()
	}()
	return d, nil
}

func (d *Daemon) Wait() error {
	status := d.App.Wait()
	if err := d.App.Error(); err != nil {
		return err
	}
	if status != svcutil.ExitSuccess {
		return fmt.Errorf("syncthing stopped with status %v", status)
	}
	return nil
}

func (d *Daemon) StartInterceptor(ctx context.Context, in *interceptor.Interceptor) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- eventloop.Loop{
			Logger:  d.Events,
			Folder:  in.Folder,
			Handler: in,
		}.Run(ctx)
	}()
	return done
}

func resolvePaths(configFile string) (Paths, error) {
	if configFile == "" {
		return Paths{}, errors.New("config path is required")
	}
	abs, err := filepath.Abs(configFile)
	if err != nil {
		return Paths{}, err
	}
	state := filepath.Join(filepath.Dir(abs), "state")
	return Paths{ConfigFile: abs, StateDir: state}, nil
}

func oracleDevice(cfg appconfig.File) (protocol.DeviceID, string, error) {
	for _, node := range cfg.RemoteNodes {
		if node.Introducer {
			id, err := protocol.DeviceIDFromString(node.DeviceID)
			return id, node.Name, err
		}
	}
	id, err := protocol.DeviceIDFromString(cfg.RemoteNodes[0].DeviceID)
	return id, cfg.RemoteNodes[0].Name, err
}

func ensureSyncIgnores(root string) error {
	if root == "" {
		return nil
	}
	path := filepath.Join(root, ".stignore")
	existing := map[string]struct{}{}
	var lines []string
	if bs, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(bs), "\n") {
			if line == "" {
				continue
			}
			existing[line] = struct{}{}
			lines = append(lines, line)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, line := range []string{
		".obsidian/**",
		"*.sync-conflict-*",
		".obsyncd-*",
	} {
		if _, ok := existing[line]; !ok {
			lines = append(lines, line)
		}
	}
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		return err
	}
	return nil
}
