package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	appconfig "obsyncd/internal/config"
	eventloop "obsyncd/internal/events"
	"obsyncd/internal/interceptor"
	"obsyncd/internal/ipc"

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

	db, err := backend.Open(locations.Get(locations.Database), backend.TuningSmall)
	if err != nil {
		loggerCancel()
		return nil, fmt.Errorf("open syncthing db: %w", err)
	}
	app, err := syncthing.New(stCfg, db, evLogger, cert, syncthing.Options{
		NoUpgrade: true,
	})
	if err != nil {
		_ = db.Close()
		loggerCancel()
		return nil, err
	}
	if err := app.Start(); err != nil {
		loggerCancel()
		return nil, err
	}
	oracleID, oracleName, err := oracleDevice(appCfg)
	if err != nil {
		app.Stop(svcutil.ExitError)
		loggerCancel()
		return nil, err
	}
	rpcServer, err := ipc.Start(ctx, "", app, appconfig.DefaultFolderID, oracleID, oracleName)
	if err != nil {
		app.Stop(svcutil.ExitError)
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
