package ipc

import (
	"context"
	"errors"
	"net"
	"net/rpc"
	"os"
	"path/filepath"

	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/syncthing"
)

type Server struct {
	app      *syncthing.App
	folderID string
	oracleID protocol.DeviceID
	oracle   string
	socket   string
	listener net.Listener
}

func DefaultSocketPath() string {
	if path := os.Getenv("OBSYNCD_SOCKET"); path != "" {
		return path
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "obsyncd.sock")
	}
	return filepath.Join(dir, "obsyncd", "obsyncd.sock")
}

func Start(ctx context.Context, socket string, app *syncthing.App, folderID string, oracleID protocol.DeviceID, oracleName string) (*Server, error) {
	if socket == "" {
		socket = DefaultSocketPath()
	}
	if app == nil || app.Internals == nil {
		return nil, errors.New("syncthing app not ready")
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}

	s := &Server{
		app:      app,
		folderID: folderID,
		oracleID: oracleID,
		oracle:   oracleName,
		socket:   socket,
		listener: ln,
	}
	rpcServer := rpc.NewServer()
	if err := rpcServer.RegisterName("Daemon", s); err != nil {
		_ = ln.Close()
		return nil, err
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = os.Remove(socket)
	}()
	go rpcServer.Accept(ln)
	return s, nil
}

func (s *Server) Socket() string {
	return s.socket
}

func (s *Server) Status(_ StatusArgs, reply *StatusReply) error {
	state, when, err := s.app.Internals.FolderState(s.folderID)
	if err != nil {
		return err
	}
	*reply = StatusReply{
		FolderID:        s.folderID,
		FolderState:     state,
		FolderStateTime: when.Format("2006-01-02 15:04:05 MST"),
		OracleName:      s.oracle,
		OracleDeviceID:  s.oracleID.String(),
		OracleConnected: s.app.Internals.IsConnectedTo(s.oracleID),
	}
	return nil
}

func (s *Server) Rescan(args RescanArgs, reply *RescanReply) error {
	if len(args.Paths) == 0 {
		if err := s.app.Internals.ScanFolderSubdirs(s.folderID, nil); err != nil {
			return err
		}
	} else if err := s.app.Internals.ScanFolderSubdirs(s.folderID, args.Paths); err != nil {
		return err
	}
	*reply = RescanReply{FolderID: s.folderID, Paths: args.Paths, OK: true}
	return nil
}
