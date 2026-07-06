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
	ClearPending(ctx context.Context, folder, canonicalRel string) error
	Pending(ctx context.Context) ([]statestore.Pending, error)
}

type BaseStore interface {
	Base(ctx context.Context, folder, path string) (string, bool, error)
}

type Proposal struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Device      string `json:"device"`
	Path        string `json:"path"`
	BaseHash    string `json:"base_hash"`
	ContentHash string `json:"content_hash"`
	Content     string `json:"content"`
	Resolve     bool   `json:"resolve,omitempty"`
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
	Root           string
	ProposalDir    string
	ProposalFolder string
	Folder         string
	DeviceID       string
	Store          Store
	Controller     Controller
	Interval       time.Duration
}

type Hub struct {
	Root           string
	ProposalDir    string
	Folder         string
	ProposalFolder string
	DeviceID       string
	TargetDevices  []string
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

const proposalSettleDelay = 3 * time.Second

func (s *Submitter) Run(ctx context.Context) error {
	if s.Store == nil {
		return errors.New("proposal submitter store is nil")
	}
	return every(ctx, s.Interval, s.scan)
}

func (s *Submitter) scan(ctx context.Context) error {
	cleanupTransportFiles(s.ProposalDir)
	return walkMarkdown(s.Root, func(rel, path string) error {
		if pending, err := s.Store.HasPending(ctx, s.Folder, rel); err != nil || pending {
			return err
		}
		wrote, err := SubmitPathChanged(ctx, s.Root, s.ProposalDir, s.Folder, s.DeviceID, s.Store, rel)
		if err != nil {
			return err
		}
		if wrote && s.Controller != nil && s.ProposalFolder != "" {
			_ = s.Controller.Rescan(ctx, s.ProposalFolder, nil)
		}
		return nil
	})
}

func SubmitPath(ctx context.Context, root, proposalDir, folder, deviceID string, store BaseStore, rel string) error {
	_, err := SubmitPathChanged(ctx, root, proposalDir, folder, deviceID, store, rel)
	return err
}

func SubmitPathChanged(ctx context.Context, root, proposalDir, folder, deviceID string, store BaseStore, rel string) (bool, error) {
	path, err := safeJoin(root, rel)
	if err != nil {
		return false, err
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		return false, nil
	}
	base, ok, err := store.Base(ctx, folder, rel)
	if err != nil {
		return false, err
	}
	if ok && hashString(base) == hashBytes(bs) {
		return false, nil
	}
	content := string(bs)
	return SubmitContentChanged(ctx, proposalDir, folder, deviceID, store, rel, content, false)
}

func SubmitContent(ctx context.Context, proposalDir, folder, deviceID string, store BaseStore, rel, content string, force bool) error {
	_, err := SubmitContentChanged(ctx, proposalDir, folder, deviceID, store, rel, content, force)
	return err
}

func SubmitContentChanged(ctx context.Context, proposalDir, folder, deviceID string, store BaseStore, rel, content string, force bool) (bool, error) {
	base, ok, err := store.Base(ctx, folder, rel)
	if err != nil {
		return false, err
	}
	if ok && hashString(base) == hashString(content) && !force {
		return false, nil
	}
	baseHash := ""
	if ok {
		baseHash = hashString(base)
	}
	p := Proposal{
		Type: "proposal", Device: deviceID, Path: filepath.ToSlash(rel),
		BaseHash: baseHash, ContentHash: hashString(content), Content: content,
		Resolve:   force,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if force {
		p.ID = hashString(p.Device + "\x00" + p.Path + "\x00" + p.BaseHash + "\x00" + p.ContentHash + "\x00" + p.CreatedAt)
	} else {
		p.ID = hashString(p.Device + "\x00" + p.Path + "\x00" + p.BaseHash + "\x00" + p.ContentHash)
	}
	return true, writeJSON(filepath.Join(proposalDir, "proposal-"+p.ID+".json"), p)
}

func GlobalConflicts(proposalDir string) ([]Conflict, error) {
	entries, err := os.ReadDir(proposalDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	byKey := map[string]Conflict{}
	var order []string
	var out []Conflict
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "conflict-") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var c Conflict
		if err := readJSON(filepath.Join(proposalDir, entry.Name()), &c); err != nil || c.Type != "conflict" {
			continue
		}
		path := filepath.ToSlash(filepath.Clean(c.Path))
		key := c.TargetDevice + "\x00" + path
		c.Path = path
		if prev, ok := byKey[key]; ok {
			if c.CreatedAt > prev.CreatedAt {
				byKey[key] = c
			}
		} else {
			byKey[key] = c
			order = append(order, key)
		}
	}
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return out, nil
}

func LocalPending(proposalDir, deviceID string) ([]string, error) {
	entries, err := os.ReadDir(proposalDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "proposal-") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var p Proposal
		if err := readJSON(filepath.Join(proposalDir, entry.Name()), &p); err != nil || p.Type != "proposal" || p.Device != deviceID {
			continue
		}
		path := filepath.ToSlash(filepath.Clean(p.Path))
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out, nil
}

func hasLocalPending(proposalDir, deviceID string) bool {
	paths, err := LocalPending(proposalDir, deviceID)
	return err == nil && len(paths) > 0
}

func (h Hub) Run(ctx context.Context) error {
	if h.Store == nil {
		return errors.New("proposal hub store is nil")
	}
	return every(ctx, h.Interval, h.scan)
}

func (h Hub) scan(ctx context.Context) error {
	cleanupTransportFiles(h.ProposalDir)
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
	if !p.Resolve && !proposalSettled(p) {
		return nil
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
	if !p.Resolve && serverHash != p.ContentHash && serverHash == p.BaseHash && (h.hasCompetingProposal(p) || h.hasConflictForPath(p.Path)) {
		return h.writeConflict(ctx, proposalPath, p, serverHash, string(server))
	}
	if serverHash == p.ContentHash {
		if err := h.Store.SaveBase(ctx, h.Folder, p.Path, p.Content); err != nil {
			return err
		}
		created := time.Now().UTC().Format(time.RFC3339Nano)
		if err := h.writeAccepted(ctx, p, p.Content, p.ContentHash, created); err != nil {
			return err
		}
		_ = os.Remove(proposalPath)
		if h.Controller != nil {
			_ = h.Controller.Rescan(ctx, h.ProposalFolder, []string{filepath.Base(proposalPath)})
		}
		h.removeConflicts(ctx, p.Path)
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
		created := time.Now().UTC().Format(time.RFC3339Nano)
		if err := h.writeAccepted(ctx, p, p.Content, p.ContentHash, created); err != nil {
			return err
		}
		_ = os.Remove(proposalPath)
		if h.Controller != nil {
			_ = h.Controller.Rescan(ctx, h.Folder, []string{p.Path})
			_ = h.Controller.Rescan(ctx, h.ProposalFolder, []string{filepath.Base(proposalPath)})
		}
		h.removeConflicts(ctx, p.Path)
		log.Printf("OBSYNCD HUB: accepted %s from %s", p.Path, short(p.Device))
		return nil
	}
	return h.writeConflict(ctx, proposalPath, p, serverHash, string(server))
}

func (h Hub) writeConflict(ctx context.Context, proposalPath string, p Proposal, serverHash, serverContent string) error {
	h.removeTargetConflicts(ctx, p.Path, p.Device)
	c := Conflict{
		Type: "conflict", ID: p.ID, TargetDevice: p.Device, Path: p.Path,
		ServerHash: serverHash, ProposalHash: p.ContentHash,
		ServerContent: serverContent, ClientContent: p.Content,
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

func (h Hub) writeAccepted(ctx context.Context, p Proposal, content, contentHash, created string) error {
	targets := h.acceptTargets(p.Device)
	var names []string
	for _, target := range targets {
		ack := Accepted{
			Type: "accepted", ID: p.ID, TargetDevice: target, Path: p.Path,
			ContentHash: contentHash, Content: content,
			CreatedAt: created,
		}
		name := "accepted-" + deviceKey(target) + "-" + p.ID + ".json"
		if err := writeJSON(filepath.Join(h.ProposalDir, name), ack); err != nil {
			return err
		}
		names = append(names, name)
	}
	if h.Controller != nil {
		_ = h.Controller.Rescan(ctx, h.ProposalFolder, names)
	}
	return nil
}

func (h Hub) acceptTargets(proposer string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, target := range append([]string{proposer}, h.TargetDevices...) {
		if target == "" || target == h.DeviceID {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	return out
}

func proposalSettled(p Proposal) bool {
	created, err := time.Parse(time.RFC3339Nano, p.CreatedAt)
	if err != nil {
		return true
	}
	return time.Since(created) >= proposalSettleDelay
}

func (h Hub) hasCompetingProposal(p Proposal) bool {
	entries, err := os.ReadDir(h.ProposalDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "proposal-") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(h.ProposalDir, entry.Name())
		var other Proposal
		if err := readJSON(path, &other); err != nil || other.Type != "proposal" || other.ID == p.ID || other.Resolve {
			continue
		}
		if other.Path == p.Path && other.BaseHash == p.BaseHash && other.ContentHash != p.ContentHash {
			return true
		}
	}
	return false
}

func (h Hub) hasConflictForPath(rel string) bool {
	entries, err := os.ReadDir(h.ProposalDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "conflict-") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var c Conflict
		if err := readJSON(filepath.Join(h.ProposalDir, entry.Name()), &c); err != nil || c.Type != "conflict" {
			continue
		}
		if c.Path == rel {
			return true
		}
	}
	return false
}

func (c ConflictIngest) Run(ctx context.Context) error {
	if c.Store == nil {
		return errors.New("conflict ingest store is nil")
	}
	return every(ctx, c.Interval, c.scan)
}

func (c ConflictIngest) scan(ctx context.Context) error {
	cleanupTransportFiles(c.ProposalDir)
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
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if hashBytes(bs) != ack.ContentHash {
		if err := atomicWrite(canonical, []byte(ack.Content), 0o644); err != nil {
			return err
		}
		bs = []byte(ack.Content)
	}
	if err := c.Store.SaveBase(ctx, c.Folder, ack.Path, string(bs)); err != nil {
		return err
	}
	if err := c.Store.ClearPending(ctx, c.Folder, ack.Path); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(c.ProposalDir, "proposal-"+ack.ID+".json"))
	_ = os.Remove(path)
	if c.Controller != nil {
		pending, err := c.Store.Pending(ctx)
		if err != nil {
			return err
		}
		if len(pending) == 0 && !hasLocalPending(c.ProposalDir, c.DeviceID) {
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
	if base, ok, err := c.Store.Base(ctx, c.Folder, job.Path); err != nil {
		return err
	} else if ok && job.ServerHash != "" && hashString(base) != job.ServerHash {
		return nil
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
	if c.Controller != nil {
		_ = c.Controller.Pause(ctx, c.Folder)
	}
	log.Printf("OBSYNCD CONFLICT: %s differs from hub; run obsyncctl", job.Path)
	return nil
}

func (h Hub) removeConflicts(ctx context.Context, rel string) {
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
		if c.Path == rel {
			_ = os.Remove(path)
			removed = append(removed, entry.Name())
		}
	}
	if len(removed) > 0 && h.Controller != nil {
		_ = h.Controller.Rescan(ctx, h.ProposalFolder, removed)
	}
}

func (h Hub) removeTargetConflicts(ctx context.Context, rel, device string) {
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

func cleanupTransportFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-10 * time.Minute)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, ".syncthing") && !(strings.HasPrefix(name, "accepted-") && filepath.Ext(name) == ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(path)
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

func deviceKey(id string) string {
	hash := hashString(id)
	if len(hash) < 12 {
		return hash
	}
	return hash[:12]
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
