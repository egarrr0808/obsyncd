package statestore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveBaseAndBase(t *testing.T) {
	store := New(t.TempDir())
	if err := store.SaveBase(context.Background(), "obsidian", "note.md", "base\n"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Base(context.Background(), "obsidian", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "base\n" {
		t.Fatalf("base = %q %t", got, ok)
	}
}

func TestStageAndResolveRemote(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	mustWrite(t, filepath.Join(root, "note.md"), "local\n")
	artifact := filepath.Join(root, "note.sync-conflict-20260704-120000-REMOTE.md")
	mustWrite(t, artifact, "remote\n")
	if _, err := store.Stage(context.Background(), "obsidian", "note.md", artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("artifact remains: %v", err)
	}
	pending, err := store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Canonical != "note.md" {
		t.Fatalf("pending = %#v", pending)
	}
	if _, err := store.Resolve(context.Background(), "obsidian", "note.md", "remote"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "remote\n" {
		t.Fatalf("canonical = %q", got)
	}
	pending, err = store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending remains: %#v", pending)
	}
}

func TestStageIsIdempotentForExistingPending(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	first := filepath.Join(root, "first.tmp")
	second := filepath.Join(root, "second.tmp")
	mustWrite(t, first, "first\n")
	mustWrite(t, second, "second\n")
	if _, err := store.Stage(context.Background(), "obsidian", "note.md", first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stage(context.Background(), "obsidian", "note.md", second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("second artifact remains: %v", err)
	}
	pending, err := store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %#v", pending)
	}
	staged, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pending[0].Staged)))
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != "first\n" {
		t.Fatalf("staged overwritten: %q", staged)
	}
}

func TestClearPendingRemovesMetadataAndStagedFile(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	artifact := filepath.Join(root, "remote.tmp")
	mustWrite(t, artifact, "remote\n")
	p, err := store.Stage(context.Background(), "obsidian", "note.md", artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClearPending(context.Background(), "obsidian", "note.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p.Staged))); !os.IsNotExist(err) {
		t.Fatalf("staged remains: %v", err)
	}
	pending, err := store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending remains: %#v", pending)
	}
}

func TestResolveDeletedCanonicalKeepLocalDeletion(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	artifact := filepath.Join(root, "note.sync-conflict-20260704-120000-REMOTE.md")
	mustWrite(t, artifact, "remote\n")
	if _, err := store.Stage(context.Background(), "obsidian", "note.md", artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), "obsidian", "note.md", "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "note.md")); !os.IsNotExist(err) {
		t.Fatalf("canonical exists or stat failed: %v", err)
	}
	pending, err := store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending remains: %#v", pending)
	}
}

func TestResolveDeletedCanonicalRestoreRemote(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	artifact := filepath.Join(root, "note.sync-conflict-20260704-120000-REMOTE.md")
	mustWrite(t, artifact, "remote\n")
	if _, err := store.Stage(context.Background(), "obsidian", "note.md", artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), "obsidian", "note.md", "remote"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "remote\n" {
		t.Fatalf("canonical = %q", got)
	}
}

func TestResolveLocalDoesNotAdvanceBase(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	mustWrite(t, filepath.Join(root, "note.md"), "local\n")
	if err := store.SaveBase(context.Background(), "obsidian", "note.md", "base\n"); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(root, "note.sync-conflict-20260704-120000-REMOTE.md")
	mustWrite(t, artifact, "remote\n")
	if _, err := store.Stage(context.Background(), "obsidian", "note.md", artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), "obsidian", "note.md", "local"); err != nil {
		t.Fatal(err)
	}
	base, ok, err := store.Base(context.Background(), "obsidian", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || base != "base\n" {
		t.Fatalf("base advanced: %q %t", base, ok)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
