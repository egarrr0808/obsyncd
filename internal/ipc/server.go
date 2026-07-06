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
	globalConflicts := s.globalConflicts()
	manual := pendingPaths(pending)
	paused := s.folderPaused()
	if paused && len(pending) == 0 && len(localPending) == 0 && len(globalConflicts) == 0 {
		_ = s.setPaused(false)
		paused = false
	}
	if state == "" && len(pending) > 0 {
		state = "paused (pending resolution)"
	} else if state == "" && len(globalConflicts) > 0 {
		state = "pending shared resolution"
	} else if state == "" && len(localPending) > 0 {
		state = "paused (awaiting hub approval)"
	} else if state == "" && paused {
		state = "paused"
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
		GlobalConflicts: globalConflicts,
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
	resolvedByHub := args.Action == "local" || args.Action == "remote" || args.Action == "submerge" || args.Action == "manual"
	if resolvedByHub {
		content, err := readOptional(s.root, path)
		if err != nil {
			return err
		}
		if err := proposal.SubmitContent(context.Background(), s.proposals, s.folderID, s.deviceID, s.store, path, content, true); err != nil {
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

func (s *Server) ResolveGlobal(args ResolveArgs, reply *ResolveReply) error {
	if s.store == nil {
		return errors.New("state store is nil")
	}
	if err := validateResolveAction(args.Action); err != nil {
		return err
	}
	rel := filepath.ToSlash(filepath.Clean(args.Path))
	conflict, ok := s.globalConflict(rel)
	if !ok {
		return errors.New("global conflict not found")
	}
	canonical, err := safeJoin(s.root, rel)
	if err != nil {
		return err
	}
	local, err := os.ReadFile(canonical)
	localMissing := os.IsNotExist(err)
	if err != nil && !localMissing {
		return err
	}
	var content string
	switch args.Action {
	case "local", "manual":
		if localMissing {
			content = ""
		} else {
			content = string(local)
		}
	case "remote":
		content = conflict.ServerContent
	case "submerge":
		content = string(local)
		if content != "" && !strings.HasSuffix(content, "\n") && conflict.ServerContent != "" {
			content += "\n"
		}
		content += conflict.ServerContent
	}
	if args.Action != "local" && args.Action != "manual" {
		if err := atomicWrite(canonical, []byte(content), 0o644); err != nil {
			return err
		}
		if err := s.store.SaveBase(context.Background(), s.folderID, rel, content); err != nil {
			return err
		}
	}
	if err := proposal.SubmitContent(context.Background(), s.proposals, s.folderID, s.deviceID, s.store, rel, content, true); err != nil {
		return err
	}
	_ = s.app.Internals.ScanFolderSubdirs("obsyncd-proposals", nil)
	*reply = ResolveReply{Path: rel, OK: true}
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

func (s *Server) folderPaused() bool {
	if s.cfg == nil {
		return false
	}
	fcfg, ok := s.cfg.Folder(s.folderID)
	return ok && fcfg.Paused
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

func (s *Server) globalConflicts() []GlobalConflict {
	conflicts, err := proposal.GlobalConflicts(s.proposals)
	if err != nil {
		return nil
	}
	out := make([]GlobalConflict, 0, len(conflicts))
	for _, c := range conflicts {
		out = append(out, GlobalConflict{
			Path:          c.Path,
			TargetDevice:  c.TargetDevice,
			ServerContent: c.ServerContent,
			ClientContent: c.ClientContent,
		})
	}
	return out
}

func (s *Server) globalConflict(rel string) (GlobalConflict, bool) {
	for _, c := range s.globalConflicts() {
		if c.Path == rel {
			return c, true
		}
	}
	return GlobalConflict{}, false
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

func validateResolveAction(action string) error {
	switch action {
	case "local", "remote", "submerge", "manual":
		return nil
	default:
		return errors.New("unknown resolve action")
	}
}

func safeJoin(root, rel string) (string, error) {
	if root == "" {
		return "", errors.New("root is empty")
	}
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("unsafe relative path")
	}
	full := filepath.Join(root, clean)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	relToRoot, err := filepath.Rel(rootAbs, fullAbs)
	if err != nil {
		return "", err
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes root")
	}
	return fullAbs, nil
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".obsyncd-rpc-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func readOptional(root, rel string) (string, error) {
	path, err := safeJoin(root, rel)
	if err != nil {
		return "", err
	}
	bs, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(bs), nil
}
