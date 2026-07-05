package proposal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obsyncd/internal/statestore"
)

type Controller interface {
	Pause(ctx context.Context, folder string) error
	Resume(ctx context.Context, folder string) error
	Rescan(ctx context.Context, folder string, paths []string) error
}

type Store interface {
	Base(ctx context.Context, folder, path string) (string, bool, error)
	SaveBase(ctx context.Context, folder, path, content string) error
	Stage(ctx context.Context, folder, canonicalRel, artifactPath string) (statestore.Pending, error)
	HasPending(ctx context.Context, folder, canonicalRel string) (bool, error)
	Pending(ctx context.Context) ([]statestore.Pending, error)
}

type Proposal struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Device      string `json:"device"`
	Path        string `json:"path"`
	BaseHash    string `json:"base_hash"`
	ContentHash string `json:"content_hash"`
	Content     string `json:"content"`
	CreatedAt   string `json:"created_at"`
}

type Conflict struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	TargetDevice  string `json:"target_device"`
	Path          string `json:"path"`
	ServerHash    string `json:"server_hash"`
	ProposalHash  string `json:"proposal_hash"`
	ServerContent string `json:"server_content"`
	ClientContent string `json:"client_content"`
	CreatedAt     string `json:"created_at"`
}

type Accepted struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	TargetDevice string `json:"target_device"`
	Path         string `json:"path"`
	ContentHash  string `json:"content_hash"`
	Content      string `json:"content"`
	CreatedAt    string `json:"created_at"`
}

type Submitter struct {
	Root        string
	ProposalDir string
	Folder      string
	DeviceID    string
	Store       Store
	Controller  Controller
	Interval    time.Duration
}

type Hub struct {
	Root           string
	ProposalDir    string
	Folder         string
	ProposalFolder string
	DeviceID       string
	Store          Store
	Controller     Controller
	Interval       time.Duration
}

type ConflictIngest struct {
	Root           string
	ProposalDir    string
	Folder         string
	ProposalFolder string
	DeviceID       string
	Store          Store
	Controller     Controller
	Interval       time.Duration
}

func (s *Submitter) Run(ctx context.Context) error {
	if s.Store == nil {
		return errors.New("proposal submitter store is nil")
	}
	return every(ctx, s.Interval, s.scan)
}

func (s *Submitter) scan(ctx context.Context) error {
	return walkMarkdown(s.Root, func(rel, path string) error {
		if pending, err := s.Store.HasPending(ctx, s.Folder, rel); err != nil || pending {
			return err
		}
		return SubmitPath(ctx, s.Root, s.ProposalDir, s.Folder, s.DeviceID, s.Store, rel)
	})
}

func SubmitPath(ctx context.Context, root, proposalDir, folder, deviceID string, store Store, rel string) error {
	path, err := safeJoin(root, rel)
	if err != nil {
		return err
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	base, ok, err := store.Base(ctx, folder, rel)
	if err != nil {
		return err
	}
	if ok && hashString(base) == hashBytes(bs) {
		return nil
	}
	baseHash := ""
	if ok {
		baseHash = hashString(base)
	}
	content := string(bs)
	p := Proposal{
		Type: "proposal", Device: deviceID, Path: filepath.ToSlash(rel),
		BaseHash: baseHash, ContentHash: hashString(content), Content: content,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	p.ID = hashString(p.Device + "\x00" + p.Path + "\x00" + p.BaseHash + "\x00" + p.ContentHash)
	return writeJSON(filepath.Join(proposalDir, "proposal-"+p.ID+".json"), p)
}

func (h Hub) Run(ctx context.Context) error {
	if h.Store == nil {
		return errors.New("proposal hub store is nil")
	}
	return every(ctx, h.Interval, h.scan)
}

func (h Hub) scan(ctx context.Context) error {
	entries, err := os.ReadDir(h.ProposalDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "proposal-") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(h.ProposalDir, entry.Name())
		var p Proposal
		if err := readJSON(path, &p); err != nil || p.Type != "proposal" || p.Device == h.DeviceID {
			continue
		}
		if err := h.handle(ctx, path, p); err != nil {
			log.Printf("obsyncd hub proposal failed for %s: %v", p.Path, err)
		}
	}
	return nil
}

func (h Hub) handle(ctx context.Context, proposalPath string, p Proposal) error {
	if err := validateRel(p.Path); err != nil {
		return err
	}
	canonical, err := safeJoin(h.Root, p.Path)
	if err != nil {
		return err
	}
	server, err := os.ReadFile(canonical)
	serverMissing := os.IsNotExist(err)
	if err != nil && !serverMissing {
		return err
	}
	serverHash := ""
	if !serverMissing {
		serverHash = hashBytes(server)
	}
	if serverHash == p.ContentHash {
		if err := h.Store.SaveBase(ctx, h.Folder, p.Path, p.Content); err != nil {
			return err
		}
		ack := Accepted{
			Type: "accepted", ID: p.ID, TargetDevice: p.Device, Path: p.Path,
			ContentHash: p.ContentHash, Content: p.Content,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := writeJSON(filepath.Join(h.ProposalDir, "accepted-"+p.ID+".json"), ack); err != nil {
			return err
		}
		_ = os.Remove(proposalPath)
		if h.Controller != nil {
			_ = h.Controller.Rescan(ctx, h.ProposalFolder, []string{filepath.Base(proposalPath)})
		}
		h.removeConflicts(ctx, p.Path, p.Device)
		log.Printf("OBSYNCD HUB: confirmed %s from %s", p.Path, short(p.Device))
		return nil
	}
	if serverHash == p.BaseHash {
		if err := atomicWrite(canonical, []byte(p.Content), 0o644); err != nil {
			return err
		}
		if err := h.Store.SaveBase(ctx, h.Folder, p.Path, p.Content); err != nil {
			return err
		}
		ack := Accepted{
			Type: "accepted", ID: p.ID, TargetDevice: p.Device, Path: p.Path,
			ContentHash: p.ContentHash, Content: p.Content,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := writeJSON(filepath.Join(h.ProposalDir, "accepted-"+p.ID+".json"), ack); err != nil {
			return err
		}
		_ = os.Remove(proposalPath)
		if h.Controller != nil {
			_ = h.Controller.Rescan(ctx, h.Folder, []string{p.Path})
			_ = h.Controller.Rescan(ctx, h.ProposalFolder, []string{filepath.Base(proposalPath)})
		}
		h.removeConflicts(ctx, p.Path, p.Device)
		log.Printf("OBSYNCD HUB: accepted %s from %s", p.Path, short(p.Device))
		return nil
	}
	c := Conflict{
		Type: "conflict", ID: p.ID, TargetDevice: p.Device, Path: p.Path,
		ServerHash: serverHash, ProposalHash: p.ContentHash,
		ServerContent: string(server), ClientContent: p.Content,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSON(filepath.Join(h.ProposalDir, "conflict-"+p.ID+".json"), c); err != nil {
		return err
	}
	_ = os.Remove(proposalPath)
	if h.Controller != nil {
		_ = h.Controller.Rescan(ctx, h.ProposalFolder, nil)
	}
	log.Printf("OBSYNCD HUB: conflict %s for %s; waiting client resolution", p.Path, short(p.Device))
	return nil
}

func (c ConflictIngest) Run(ctx context.Context) error {
	if c.Store == nil {
		return errors.New("conflict ingest store is nil")
	}
	return every(ctx, c.Interval, c.scan)
}

func (c ConflictIngest) scan(ctx context.Context) error {
	entries, err := os.ReadDir(c.ProposalDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "conflict-") || filepath.Ext(entry.Name()) != ".json" {
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "accepted-") && filepath.Ext(entry.Name()) == ".json" {
				if err := c.handleAccepted(ctx, filepath.Join(c.ProposalDir, entry.Name())); err != nil {
					log.Printf("obsyncd accept ingest failed: %v", err)
				}
			}
			continue
		}
		path := filepath.Join(c.ProposalDir, entry.Name())
		var job Conflict
		if err := readJSON(path, &job); err != nil || job.Type != "conflict" || job.TargetDevice != c.DeviceID {
			continue
		}
		if err := c.handle(ctx, path, job); err != nil {
			log.Printf("obsyncd conflict ingest failed for %s: %v", job.Path, err)
		}
	}
	return nil
}

func (c ConflictIngest) handleAccepted(ctx context.Context, path string) error {
	var ack Accepted
	if err := readJSON(path, &ack); err != nil || ack.Type != "accepted" || ack.TargetDevice != c.DeviceID {
		return err
	}
	canonical, err := safeJoin(c.Root, ack.Path)
	if err != nil {
		return err
	}
	bs, err := os.ReadFile(canonical)
	if err != nil {
		return err
	}
	if hashBytes(bs) != ack.ContentHash {
		return nil
	}
	if err := c.Store.SaveBase(ctx, c.Folder, ack.Path, string(bs)); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(c.ProposalDir, "proposal-"+ack.ID+".json"))
	_ = os.Remove(path)
	if c.Controller != nil {
		pending, err := c.Store.Pending(ctx)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			_ = c.Controller.Resume(ctx, c.Folder)
		}
		_ = c.Controller.Rescan(ctx, c.ProposalFolder, []string{filepath.Base(path), "proposal-" + ack.ID + ".json"})
	}
	log.Printf("OBSYNCD ACCEPTED: %s stored by hub", ack.Path)
	return nil
}

func (c ConflictIngest) handle(ctx context.Context, jobPath string, job Conflict) error {
	if pending, err := c.Store.HasPending(ctx, c.Folder, job.Path); err != nil || pending {
		return err
	}
	canonical, err := safeJoin(c.Root, job.Path)
	if err != nil {
		return err
	}
	local, err := os.ReadFile(canonical)
	if os.IsNotExist(err) {
		if err := atomicWrite(canonical, []byte(job.ClientContent), 0o644); err != nil {
			return err
		}
		local = []byte(job.ClientContent)
	} else if err != nil {
		return err
	}
	if job.ProposalHash != "" && hashBytes(local) != job.ProposalHash {
		_ = os.Remove(jobPath)
		if c.Controller != nil {
			_ = c.Controller.Rescan(ctx, c.ProposalFolder, []string{filepath.Base(jobPath)})
		}
		log.Printf("OBSYNCD CONFLICT: ignored stale conflict for %s", job.Path)
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(canonical), ".obsyncd-server-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(job.ServerContent); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if _, err := c.Store.Stage(ctx, c.Folder, job.Path, tmpName); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := c.Store.SaveBase(ctx, c.Folder, job.Path, job.ServerContent); err != nil {
		return err
	}
	_ = os.Remove(jobPath)
	if c.Controller != nil {
		_ = c.Controller.Pause(ctx, c.Folder)
		_ = c.Controller.Rescan(ctx, c.ProposalFolder, []string{filepath.Base(jobPath)})
	}
	log.Printf("OBSYNCD CONFLICT: %s differs from hub; run obsyncctl", job.Path)
	return nil
}

func (h Hub) removeConflicts(ctx context.Context, rel, device string) {
	entries, err := os.ReadDir(h.ProposalDir)
	if err != nil {
		return
	}
	var removed []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "conflict-") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(h.ProposalDir, entry.Name())
		var c Conflict
		if err := readJSON(path, &c); err != nil {
			continue
		}
		if c.Path == rel && c.TargetDevice == device {
			_ = os.Remove(path)
			removed = append(removed, entry.Name())
		}
	}
	if len(removed) > 0 && h.Controller != nil {
		_ = h.Controller.Rescan(ctx, h.ProposalFolder, removed)
	}
}

func every(ctx context.Context, interval time.Duration, fn func(context.Context) error) error {
	if interval <= 0 {
		interval = time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := fn(ctx); err != nil {
			log.Printf("obsyncd proposal loop: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func walkMarkdown(root string, fn func(rel, path string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		slash := filepath.ToSlash(filepath.Clean(rel))
		if strings.HasPrefix(slash, ".obsidian/") || strings.Contains(filepath.Base(slash), ".sync-conflict-") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(slash))
		if ext != ".md" && ext != ".markdown" {
			return nil
		}
		return fn(slash, path)
	})
}

func writeJSON(path string, v any) error {
	bs, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(bs, '\n'), 0o600)
}

func readJSON(path string, v any) error {
	bs, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(bs, v)
}

func hashString(s string) string { return hashBytes([]byte(s)) }

func hashBytes(bs []byte) string {
	sum := sha256.Sum256(bs)
	return hex.EncodeToString(sum[:])
}

func short(id string) string {
	if len(id) < 6 {
		return id
	}
	return id[:6]
}

func validateRel(rel string) error {
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe relative path: %s", rel)
	}
	return nil
}

func safeJoin(root, rel string) (string, error) {
	if err := validateRel(rel); err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	inside, err := filepath.Rel(rootAbs, fullAbs)
	if err != nil {
		return "", err
	}
	if inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %s", rel)
	}
	return fullAbs, nil
}

func atomicWrite(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".obsyncd-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
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
	return os.Rename(name, path)
}
