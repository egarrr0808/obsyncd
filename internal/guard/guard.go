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
	"obsyncd/internal/proposal"
	"obsyncd/internal/statestore"

	"github.com/syncthing/notify"
	stevents "github.com/syncthing/syncthing/lib/events"
)

type Controller interface {
	Pause(ctx context.Context, folder string) error
	Rescan(ctx context.Context, folder string, paths []string) error
}

type Stager interface {
	Base(ctx context.Context, folder, path string) (string, bool, error)
	SaveBase(ctx context.Context, folder, path, content string) error
	DeleteBase(ctx context.Context, folder, path string) error
	Stage(ctx context.Context, folder, canonicalRel, artifactPath string) (statestore.Pending, error)
	HasPending(ctx context.Context, folder, canonicalRel string) (bool, error)
}

type Guard struct {
	Root           string
	StateDir       string
	Folder         string
	ProposalFolder string
	ProposalDir    string
	DeviceID       string
	Logger         stevents.Logger
	Controller     Controller
	Stager         Stager
	MaxSnapshotAge time.Duration
	BaseWait       time.Duration

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
				if err := g.snapshotLocal(ctx, path); err != nil {
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

func (g *Guard) RunSnapshotScanner(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	if err := os.MkdirAll(g.snapshotDir(), 0o700); err != nil {
		return err
	}
	seen, err := g.scanHashes()
	if err != nil {
		return err
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			next, err := g.scanHashes()
			if err != nil {
				log.Printf("obsyncd conflict guard scan failed: %v", err)
				continue
			}
			for rel, sum := range next {
				if seen[rel] == "" {
					seen[rel] = sum
					continue
				}
				if seen[rel] == sum {
					continue
				}
				seen[rel] = sum
				snapPath := g.snapshotPath(rel)
				if _, err := os.Stat(snapPath); err == nil {
					if err := g.stageChangedDirtyFile(ctx, rel); err != nil {
						log.Printf("obsyncd conflict guard stage failed for %s: %v", rel, err)
					}
					continue
				}
				if err := g.snapshotLocal(ctx, rel); err != nil {
					log.Printf("obsyncd conflict guard snapshot failed for %s: %v", rel, err)
				}
			}
			for rel := range seen {
				if _, ok := next[rel]; !ok {
					delete(seen, rel)
					_ = os.Remove(g.snapshotPath(rel))
				}
			}
		}
	}
}

func (g *Guard) RunFSWatcher(ctx context.Context) error {
	if g.Controller == nil {
		return fmt.Errorf("controller is nil")
	}
	if err := os.MkdirAll(g.snapshotDir(), 0o700); err != nil {
		return err
	}
	events := make(chan notify.EventInfo, 128)
	if err := notify.Watch(filepath.Join(g.Root, "..."), events, notify.Write, notify.Create, notify.Rename); err != nil {
		return err
	}
	defer notify.Stop(events)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-events:
			rel := relFor(g.Root, ev.Path())
			if !isMarkdown(rel) || isGenerated(rel) || isInternalRel(rel) {
				continue
			}
			if err := g.snapshotLocal(ctx, rel); err != nil {
				log.Printf("obsyncd conflict guard fs watch failed for %s: %v", rel, err)
			}
		}
	}
}

func (g *Guard) stageChangedDirtyFile(ctx context.Context, rel string) error {
	if g.Stager == nil || g.Controller == nil {
		return nil
	}
	path, err := safeJoin(g.Root, rel)
	if err != nil {
		return err
	}
	localBefore, err := os.ReadFile(g.snapshotPath(rel))
	if err != nil {
		return err
	}
	remoteNow, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(localBefore) == string(remoteNow) {
		return nil
	}
	if base, ok, err := g.Stager.Base(ctx, g.Folder, rel); err != nil {
		return err
	} else if ok && base == string(localBefore) {
		_ = os.Remove(g.snapshotPath(rel))
		return g.Stager.SaveBase(ctx, g.Folder, rel, string(remoteNow))
	} else if ok && base == string(remoteNow) {
		return os.Remove(g.snapshotPath(rel))
	}
	if pending, err := g.Stager.HasPending(ctx, g.Folder, rel); err != nil {
		return err
	} else if pending {
		if err := g.Controller.Pause(ctx, g.Folder); err != nil {
			return err
		}
		if err := atomicWrite(path, localBefore, 0o644); err != nil {
			return err
		}
		_ = os.Remove(g.snapshotPath(rel))
		return g.Controller.Rescan(ctx, g.Folder, []string{rel})
	}
	tmpRemote, err := writeRemoteTemp(path, remoteNow)
	if err != nil {
		return err
	}
	if _, err := g.Stager.Stage(ctx, g.Folder, rel, tmpRemote); err != nil {
		_ = os.Remove(tmpRemote)
		return err
	}
	if err := g.Controller.Pause(ctx, g.Folder); err != nil {
		return err
	}
	if err := atomicWrite(path, localBefore, 0o644); err != nil {
		return err
	}
	_ = os.Remove(g.snapshotPath(rel))
	log.Printf("OBSYNCD CONFLICT: %s awaiting user resolution; run obsyncctl", rel)
	return g.Controller.Rescan(ctx, g.Folder, []string{rel})
}

func (g *Guard) snapshotLocal(ctx context.Context, rel string) error {
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
	if g.Stager != nil {
		if pending, err := g.Stager.HasPending(ctx, g.Folder, rel); err != nil {
			return err
		} else if pending {
			return nil
		}
		if base, ok, err := g.Stager.Base(ctx, g.Folder, rel); err != nil {
			return err
		} else if ok && base == string(bs) {
			return nil
		} else if !ok {
			matched, err := g.waitForArrivingBase(ctx, rel, string(bs))
			if err != nil || matched {
				return err
			}
		}
	}
	if err := atomicWrite(g.snapshotPath(rel), bs, 0o600); err != nil {
		return err
	}
	if g.Controller != nil {
		log.Printf("OBSYNCD HOLD: %s changed locally; sync paused, run obsyncctl to approve", rel)
		if err := g.Controller.Pause(ctx, g.Folder); err != nil {
			return err
		}
		if g.ProposalDir != "" && g.DeviceID != "" && g.Stager != nil {
			if err := proposal.SubmitPath(ctx, g.Root, g.ProposalDir, g.Folder, g.DeviceID, g.Stager, rel); err != nil {
				return err
			}
			if g.ProposalFolder != "" {
				_ = g.Controller.Rescan(ctx, g.ProposalFolder, nil)
			}
		}
		return nil
	}
	return nil
}

func (g *Guard) scanHashes() (map[string]string, error) {
	out := make(map[string]string)
	err := filepath.WalkDir(g.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if isInternalPath(g.Root, path) {
				return filepath.SkipDir
			}
			return nil
		}
		rel := relFor(g.Root, path)
		if !isMarkdown(rel) || isGenerated(rel) || isInternalRel(rel) {
			return nil
		}
		bs, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(bs)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	return out, err
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
	remoteDeleted := os.IsNotExist(err)
	if err != nil && !remoteDeleted {
		return err
	}
	if remoteDeleted {
		remoteNow = []byte("")
	}
	if string(localBefore) == string(remoteNow) {
		return os.Remove(snapPath)
	}

	if g.Stager != nil {
		if base, ok, err := g.Stager.Base(ctx, g.Folder, rel); err != nil {
			return err
		} else if ok && base == string(localBefore) {
			_ = os.Remove(snapPath)
			if remoteDeleted {
				_ = os.Remove(path)
				return g.Stager.DeleteBase(ctx, g.Folder, rel)
			}
			return g.Stager.SaveBase(ctx, g.Folder, rel, string(remoteNow))
		} else if ok && base == string(remoteNow) {
			return os.Remove(snapPath)
		}
		if pending, err := g.Stager.HasPending(ctx, g.Folder, rel); err != nil {
			return err
		} else if pending {
			if err := g.Controller.Pause(ctx, g.Folder); err != nil {
				return err
			}
			if err := atomicWrite(path, localBefore, 0o644); err != nil {
				return err
			}
			_ = os.Remove(snapPath)
			return g.Controller.Rescan(ctx, g.Folder, []string{rel})
		}
		tmpRemote, err := writeRemoteTemp(path, remoteNow)
		if err != nil {
			return err
		}
		if _, err := g.Stager.Stage(ctx, g.Folder, rel, tmpRemote); err != nil {
			_ = os.Remove(tmpRemote)
			return err
		}
		if err := g.Controller.Pause(ctx, g.Folder); err != nil {
			return err
		}
		if err := atomicWrite(path, localBefore, 0o644); err != nil {
			return err
		}
		_ = os.Remove(snapPath)
		log.Printf("OBSYNCD CONFLICT: %s awaiting user resolution; run obsyncctl", rel)
		return g.Controller.Rescan(ctx, g.Folder, []string{rel})
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

func writeRemoteTemp(path string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".obsyncd-remote-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
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

func isInternalPath(root, path string) bool {
	return isInternalRel(relFor(root, path))
}

func isInternalRel(path string) bool {
	slash := filepath.ToSlash(filepath.Clean(path))
	return strings.HasPrefix(slash, ".obsidian/obsyncd-")
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

func (g *Guard) waitForArrivingBase(ctx context.Context, rel, content string) (bool, error) {
	wait := g.BaseWait
	if wait <= 0 {
		wait = 500 * time.Millisecond
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, nil
		case <-tick.C:
			base, ok, err := g.Stager.Base(ctx, g.Folder, rel)
			if err != nil {
				return false, err
			}
			if ok && base == content {
				return true, nil
			}
		}
	}
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

func (g *Guard) DetectRemoteOverwrite(ctx context.Context, rel string) error {
	return g.detectRemoteOverwrite(ctx, rel)
}

func (g *Guard) SnapshotPath(rel string) string {
	return g.snapshotPath(rel)
}
