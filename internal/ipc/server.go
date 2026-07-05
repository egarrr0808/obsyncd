package ipc

import (
	"context"
	"errors"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"strings"

	"obsyncd/internal/proposal"
	"obsyncd/internal/statestore"

	stconfig "github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/syncthing"
)

type Server struct {
	app       *syncthing.App
	cfg       stconfig.Wrapper
	folderID  string
	oracleID  protocol.DeviceID
	oracle    string
	root      string
	proposals string
	deviceID  string
	store     *statestore.Store
	socket    string
	listener  net.Listener
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

func Start(ctx context.Context, socket string, app *syncthing.App, cfg stconfig.Wrapper, folderID, root, proposalDir, deviceID string, store *statestore.Store, oracleID protocol.DeviceID, oracleName string) (*Server, error) {
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
		app:       app,
		cfg:       cfg,
		folderID:  folderID,
		oracleID:  oracleID,
		oracle:    oracleName,
		root:      root,
		proposals: proposalDir,
		deviceID:  deviceID,
		store:     store,
		socket:    socket,
		listener:  ln,
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
	pending := s.pendingConflicts()
	localPending := s.localPending()
	manual := pendingPaths(pending)
	if state == "" && len(pending) > 0 {
		state = "paused (pending resolution)"
	} else if state == "" && len(localPending) > 0 {
		state = "paused (awaiting hub approval)"
	}
	*reply = StatusReply{
		FolderID:        s.folderID,
		FolderState:     state,
		FolderStateTime: when.Format("2006-01-02 15:04:05 MST"),
		OracleName:      s.oracle,
		OracleDeviceID:  s.oracleID.String(),
		OracleConnected: s.app.Internals.IsConnectedTo(s.oracleID),
		ManualConflicts: manual,
		Pending:         pending,
		LocalPending:    localPending,
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

func (s *Server) Resolve(args ResolveArgs, reply *ResolveReply) error {
	if s.store == nil {
		return errors.New("state store is nil")
	}
	path, err := s.store.Resolve(context.Background(), s.folderID, args.Path, args.Action)
	if err != nil {
		return err
	}
	resolvedByHub := args.Action == "local" || args.Action == "submerge" || args.Action == "manual"
	if resolvedByHub {
		if err := proposal.SubmitPath(context.Background(), s.root, s.proposals, s.folderID, s.deviceID, s.store, path); err != nil {
			return err
		}
		_ = s.app.Internals.ScanFolderSubdirs("obsyncd-proposals", nil)
	}
	if pending := s.pendingConflicts(); len(pending) == 0 && !resolvedByHub && len(s.localPending()) == 0 {
		_ = s.setPaused(false)
	}
	if !resolvedByHub {
		if err := s.app.Internals.ScanFolderSubdirs(s.folderID, []string{path}); err != nil {
			return err
		}
	}
	*reply = ResolveReply{Path: path, OK: true}
	return nil
}

func (s *Server) setPaused(paused bool) error {
	if s.cfg == nil {
		return nil
	}
	waiter, err := s.cfg.Modify(func(cfg *stconfig.Configuration) {
		fcfg, _, ok := cfg.Folder(s.folderID)
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
	return s.cfg.Save()
}

func (s *Server) manualConflicts() []string {
	if s.store == nil {
		return nil
	}
	return pendingPaths(s.pendingConflicts())
}

func (s *Server) pendingConflicts() []PendingConflict {
	if s.store == nil {
		return nil
	}
	pending, err := s.store.Pending(context.Background())
	if err != nil {
		return nil
	}
	out := make([]PendingConflict, 0, len(pending))
	for _, p := range pending {
		out = append(out, PendingConflict{Canonical: p.Canonical, Staged: p.Staged})
	}
	return out
}

func (s *Server) localPending() []string {
	if s.store == nil {
		return nil
	}
	paths, err := proposal.LocalPending(s.proposals, s.deviceID)
	if err != nil {
		return nil
	}
	return paths
}

func pendingPaths(pending []PendingConflict) []string {
	out := make([]string, 0, len(pending))
	for _, p := range pending {
		out = append(out, p.Canonical)
	}
	return out
}

func scanManualConflicts(root string) []string {
	var files []string
	if root == "" {
		return files
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".markdown" {
			return nil
		}
		bs, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(bs), "%%OBSYNCD_CONFLICT_START%%") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err == nil {
			files = append(files, rel)
		}
		return nil
	})
	return files
}
