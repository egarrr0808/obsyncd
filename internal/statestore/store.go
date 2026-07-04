package statestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Pending struct {
	Canonical string
	Staged    string
}

type Store struct {
	Root string
}

func New(root string) *Store {
	return &Store{Root: root}
}

func (s *Store) Base(_ context.Context, _, path string) (string, bool, error) {
	bs, err := os.ReadFile(s.basePath(path))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(bs), true, nil
}

func (s *Store) SaveBase(_ context.Context, _, path, content string) error {
	if !isMarkdown(path) || isInternal(path) || strings.Contains(content, "%%OBSYNCD_CONFLICT_START%%") {
		return nil
	}
	return atomicWrite(s.basePath(path), []byte(content), 0o600)
}

func (s *Store) Stage(_ context.Context, _, canonicalRel, artifactPath string) (Pending, error) {
	if err := validateRel(canonicalRel); err != nil {
		return Pending{}, err
	}
	if p, ok, err := s.pending(canonicalRel); err != nil {
		return Pending{}, err
	} else if ok {
		_ = os.Remove(artifactPath)
		return p, nil
	}
	id := key(canonicalRel)
	stagedRel := filepath.ToSlash(filepath.Join(".obsidian", "obsyncd-staging", id+".remote"))
	stagedAbs := filepath.Join(s.Root, filepath.FromSlash(stagedRel))
	if err := os.MkdirAll(filepath.Dir(stagedAbs), 0o700); err != nil {
		return Pending{}, err
	}
	if err := os.Rename(artifactPath, stagedAbs); err != nil {
		return Pending{}, err
	}
	p := Pending{Canonical: filepath.ToSlash(filepath.Clean(canonicalRel)), Staged: stagedRel}
	bs, err := json.Marshal(p)
	if err != nil {
		return Pending{}, err
	}
	if err := atomicWrite(s.pendingPath(canonicalRel), append(bs, '\n'), 0o600); err != nil {
		return Pending{}, err
	}
	return p, nil
}

func (s *Store) HasPending(_ context.Context, _, canonicalRel string) (bool, error) {
	_, ok, err := s.pending(canonicalRel)
	return ok, err
}

func (s *Store) Pending(_ context.Context) ([]Pending, error) {
	dir := s.pendingDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Pending
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		bs, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var p Pending
		if err := json.Unmarshal(bs, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Store) Resolve(ctx context.Context, folder, canonicalRel, action string) (string, error) {
	p, ok, err := s.pending(canonicalRel)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no pending conflict for %s", canonicalRel)
	}
	canonicalPath, err := safeJoin(s.Root, canonicalRel)
	if err != nil {
		return "", err
	}
	stagedPath, err := safeJoin(s.Root, filepath.FromSlash(p.Staged))
	if err != nil {
		return "", err
	}
	local, err := os.ReadFile(canonicalPath)
	if err != nil {
		return "", err
	}
	remote, err := os.ReadFile(stagedPath)
	if err != nil {
		return "", err
	}
	var next []byte
	switch action {
	case "local":
		next = local
	case "remote":
		next = remote
	case "submerge":
		next = append(append([]byte(nil), local...), joiner(string(local), string(remote))...)
		next = append(next, remote...)
	case "manual":
		next = local
	default:
		return "", fmt.Errorf("unknown resolution action: %s", action)
	}
	if action != "manual" {
		if err := atomicWrite(canonicalPath, next, 0o644); err != nil {
			return "", err
		}
	}
	_ = os.Remove(stagedPath)
	_ = os.Remove(s.pendingPath(canonicalRel))
	if err := s.SaveBase(ctx, folder, canonicalRel, string(next)); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Clean(canonicalRel)), nil
}

func (s *Store) pending(canonicalRel string) (Pending, bool, error) {
	bs, err := os.ReadFile(s.pendingPath(canonicalRel))
	if os.IsNotExist(err) {
		return Pending{}, false, nil
	}
	if err != nil {
		return Pending{}, false, err
	}
	var p Pending
	if err := json.Unmarshal(bs, &p); err != nil {
		return Pending{}, false, err
	}
	return p, true, nil
}

func (s *Store) basePath(rel string) string {
	return filepath.Join(s.Root, ".obsidian", "obsyncd-bases", key(rel)+".base")
}

func (s *Store) pendingDir() string {
	return filepath.Join(s.Root, ".obsidian", "obsyncd-staging")
}

func (s *Store) pendingPath(rel string) string {
	return filepath.Join(s.pendingDir(), key(rel)+".json")
}

func key(rel string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(filepath.Clean(rel))))
	return hex.EncodeToString(sum[:])
}

func isMarkdown(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}

func isInternal(path string) bool {
	slash := filepath.ToSlash(filepath.Clean(path))
	return strings.HasPrefix(slash, ".obsidian/obsyncd-")
}

func joiner(local, remote string) []byte {
	if local == "" || strings.HasSuffix(local, "\n") || remote == "" {
		return nil
	}
	return []byte("\n")
}

func validateRel(rel string) error {
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe relative path: %s", rel)
	}
	return nil
}

func safeJoin(root, rel string) (string, error) {
	if root == "" {
		return "", errors.New("root is empty")
	}
	if err := validateRel(rel); err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.Clean(rel))
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
