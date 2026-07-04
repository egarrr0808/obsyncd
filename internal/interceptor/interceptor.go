package interceptor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"obsyncd/internal/diffmerge"
)

type Event struct {
	ID     int64
	Type   string
	Folder string
	Path   string
}

type EventSource interface {
	Events(ctx context.Context, since int64) ([]Event, error)
}

type Controller interface {
	Pause(ctx context.Context, folder string) error
	Resume(ctx context.Context, folder string) error
	Rescan(ctx context.Context, folder string, paths []string) error
}

type BaseStore interface {
	Base(ctx context.Context, folder, path string) (string, bool, error)
}

type Interceptor struct {
	Root       string
	Folder     string
	Events     EventSource
	Controller Controller
	Bases      BaseStore

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var conflictName = regexp.MustCompile(`(?i)^(.+)\.sync-conflict-\d{8}-\d{6}-[^/\\]+(\.md|\.markdown)$`)

func (i *Interceptor) Run(ctx context.Context) error {
	if i.Events == nil {
		return errors.New("event source is nil")
	}
	var since int64
	for {
		events, err := i.Events.Events(ctx, since)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		for _, ev := range events {
			if ev.ID > since {
				since = ev.ID
			}
			if ev.Folder != "" && i.Folder != "" && ev.Folder != i.Folder {
				continue
			}
			if !isConflictArtifact(ev.Path) {
				continue
			}
			if err := i.HandleArtifact(ctx, ev.Path); err != nil {
				return err
			}
		}
	}
}

func (i *Interceptor) HandleArtifact(ctx context.Context, artifactRel string) error {
	if i.Controller == nil {
		return errors.New("controller is nil")
	}
	if i.Bases == nil {
		return errors.New("base store is nil")
	}
	canonicalRel, ok := CanonicalPath(artifactRel)
	if !ok {
		return fmt.Errorf("not a markdown conflict artifact: %s", artifactRel)
	}

	lock := i.fileLock(canonicalRel)
	lock.Lock()
	defer lock.Unlock()

	canonicalPath, err := safeJoin(i.Root, canonicalRel)
	if err != nil {
		return err
	}
	artifactPath, err := safeJoin(i.Root, artifactRel)
	if err != nil {
		return err
	}

	if err := i.Controller.Pause(ctx, i.Folder); err != nil {
		return err
	}
	resumeErr := error(nil)
	defer func() {
		if err := i.Controller.Resume(ctx, i.Folder); err != nil {
			resumeErr = err
		}
	}()

	localBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		return err
	}
	remoteBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		return err
	}
	base, ok, err := i.Bases.Base(ctx, i.Folder, canonicalRel)
	if err != nil {
		return err
	}

	var merged string
	if ok {
		merged = diffmerge.MergeDetailed(base, string(localBytes), string(remoteBytes)).Content
	} else {
		merged = missingBaseConflict(string(localBytes), string(remoteBytes))
	}

	if err := atomicWriteFile(canonicalPath, []byte(merged), 0o644); err != nil {
		return err
	}
	if err := os.Remove(artifactPath); err != nil {
		return err
	}
	if err := i.Controller.Rescan(ctx, i.Folder, []string{canonicalRel}); err != nil {
		return err
	}
	return resumeErr
}

func CanonicalPath(path string) (string, bool) {
	clean := filepath.Clean(path)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", false
	}
	slash := filepath.ToSlash(clean)
	m := conflictName.FindStringSubmatch(slash)
	if m == nil {
		return "", false
	}
	return filepath.FromSlash(m[1] + m[2]), true
}

func isConflictArtifact(path string) bool {
	_, ok := CanonicalPath(path)
	return ok
}

func missingBaseConflict(local, remote string) string {
	eol := "\n"
	if strings.Contains(local, "\r\n") || strings.Contains(remote, "\r\n") {
		eol = "\r\n"
	}
	return diffmerge.ConflictBlock(ensureEOL(local, eol), ensureEOL(remote, eol), eol)
}

func ensureEOL(s, eol string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + eol
}

func (i *Interceptor) fileLock(path string) *sync.Mutex {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.locks == nil {
		i.locks = make(map[string]*sync.Mutex)
	}
	lock := i.locks[path]
	if lock == nil {
		lock = &sync.Mutex{}
		i.locks[path] = lock
	}
	return lock
}

func safeJoin(root, rel string) (string, error) {
	if root == "" {
		return "", errors.New("root is empty")
	}
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("unsafe relative path: %s", rel)
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
		return "", fmt.Errorf("path escapes root: %s", rel)
	}
	return fullAbs, nil
}

func atomicWriteFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".obsyncd-*")
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
