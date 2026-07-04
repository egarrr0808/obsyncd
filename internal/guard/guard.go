package guard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"obsyncd/internal/diffmerge"

	stevents "github.com/syncthing/syncthing/lib/events"
)

type Controller interface {
	Rescan(ctx context.Context, folder string, paths []string) error
}

type Guard struct {
	Root           string
	StateDir       string
	Folder         string
	Logger         stevents.Logger
	Controller     Controller
	MaxSnapshotAge time.Duration

	mu sync.Mutex
}

func (g *Guard) Run(ctx context.Context) error {
	if g.Logger == nil {
		return fmt.Errorf("event logger is nil")
	}
	if g.Controller == nil {
		return fmt.Errorf("controller is nil")
	}
	if err := os.MkdirAll(g.snapshotDir(), 0o700); err != nil {
		return err
	}

	sub := g.Logger.Subscribe(stevents.LocalChangeDetected | stevents.ItemFinished)
	defer sub.Unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-sub.C():
			if !ok {
				return nil
			}
			path, folder, ok := fileEventPath(ev)
			if !ok || !isMarkdown(path) || isGenerated(path) {
				continue
			}
			if g.Folder != "" && folder != "" && folder != g.Folder {
				continue
			}
			switch ev.Type {
			case stevents.LocalChangeDetected:
				if err := g.snapshotLocal(path); err != nil {
					log.Printf("obsyncd conflict guard snapshot failed for %s: %v", path, err)
				}
			case stevents.ItemFinished:
				if err := g.detectRemoteOverwrite(ctx, path); err != nil {
					log.Printf("obsyncd conflict guard failed for %s: %v", path, err)
				}
			}
		}
	}
}

func (g *Guard) snapshotLocal(rel string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	path, err := safeJoin(g.Root, rel)
	if err != nil {
		return err
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Contains(string(bs), "%%OBSYNCD_CONFLICT_START%%") {
		return nil
	}
	return atomicWrite(g.snapshotPath(rel), bs, 0o600)
}

func (g *Guard) detectRemoteOverwrite(ctx context.Context, rel string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	snapPath := g.snapshotPath(rel)
	if stale, err := g.snapshotStale(snapPath); err != nil {
		return err
	} else if stale {
		_ = os.Remove(snapPath)
		return nil
	}
	localBefore, err := os.ReadFile(snapPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	path, err := safeJoin(g.Root, rel)
	if err != nil {
		return err
	}
	remoteNow, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(localBefore) == string(remoteNow) {
		return os.Remove(snapPath)
	}

	localCopy, remoteCopy := copyPaths(path)
	if err := atomicWrite(localCopy, localBefore, 0o644); err != nil {
		return err
	}
	if err := atomicWrite(remoteCopy, remoteNow, 0o644); err != nil {
		return err
	}
	eol := preferredEOL(string(localBefore), string(remoteNow))
	content := "%%OBSYNCD_ATTENTION%%" + eol +
		"obsyncd detected competing edits. Run obsyncctl to resolve this file." + eol + eol +
		diffmerge.ConflictBlock(string(localBefore), string(remoteNow), eol)
	if err := atomicWrite(path, []byte(content), 0o644); err != nil {
		return err
	}
	_ = os.Remove(snapPath)
	log.Printf("OBSYNCD CONFLICT: %s changed on multiple machines; run obsyncctl. Copies: %s %s", rel, filepath.Base(localCopy), filepath.Base(remoteCopy))
	return g.Controller.Rescan(ctx, g.Folder, []string{rel, relFor(g.Root, localCopy), relFor(g.Root, remoteCopy)})
}

func fileEventPath(ev stevents.Event) (path, folder string, ok bool) {
	data, ok := ev.Data.(map[string]interface{})
	if !ok {
		return "", "", false
	}
	if typ, _ := data["type"].(string); typ != "" && typ != "file" {
		return "", "", false
	}
	if errVal, exists := data["error"]; exists && errVal != nil && errVal != "" {
		return "", "", false
	}
	folder, _ = data["folder"].(string)
	switch ev.Type {
	case stevents.ItemFinished:
		path, _ = data["item"].(string)
	case stevents.LocalChangeDetected:
		path, _ = data["path"].(string)
	default:
		return "", "", false
	}
	return path, folder, path != ""
}

func isMarkdown(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}

func isGenerated(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, ".sync-conflict-") || strings.Contains(base, ".local-v1.") || strings.Contains(base, ".remote-v2.")
}

func (g *Guard) snapshotDir() string {
	return filepath.Join(g.StateDir, "local-snapshots")
}

func (g *Guard) snapshotPath(rel string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(filepath.Clean(rel))))
	return filepath.Join(g.snapshotDir(), hex.EncodeToString(sum[:])+".snap")
}

func (g *Guard) snapshotStale(path string) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return time.Since(info.ModTime()) > g.snapshotAge(), nil
}

func (g *Guard) snapshotAge() time.Duration {
	if g.MaxSnapshotAge > 0 {
		return g.MaxSnapshotAge
	}
	return 10 * time.Minute
}

func copyPaths(path string) (string, string) {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	return stem + ".local-v1" + ext, stem + ".remote-v2" + ext
}

func preferredEOL(values ...string) string {
	for _, v := range values {
		if strings.Contains(v, "\r\n") {
			return "\r\n"
		}
	}
	return "\n"
}

func relFor(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func safeJoin(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
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

func atomicWrite(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".obsyncd-*")
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
