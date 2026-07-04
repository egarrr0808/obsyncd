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

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
