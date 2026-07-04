package guard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeController struct{ paths []string }

func (f *fakeController) Rescan(_ context.Context, _ string, paths []string) error {
	f.paths = append(f.paths, paths...)
	return nil
}

func TestDetectRemoteOverwriteCreatesCopiesAndMarker(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	path := filepath.Join(root, "note.md")
	if err := os.WriteFile(path, []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeController{}
	g := &Guard{Root: root, StateDir: state, Folder: "obsidian", Controller: ctrl}
	if err := g.snapshotLocal("note.md"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("remote edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.detectRemoteOverwrite(context.Background(), "note.md"); err != nil {
		t.Fatal(err)
	}
	localCopy, remoteCopy := copyPaths(path)
	mustContain(t, localCopy, "local edit\n")
	mustContain(t, remoteCopy, "remote edit\n")
	mustContain(t, path, "%%OBSYNCD_ATTENTION%%")
	mustContain(t, path, "%%OBSYNCD_CONFLICT_START%%")
	if len(ctrl.paths) != 3 {
		t.Fatalf("rescan paths = %#v", ctrl.paths)
	}
}

func TestDetectRemoteOverwriteClearsEqualSnapshot(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	path := filepath.Join(root, "note.md")
	if err := os.WriteFile(path, []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &Guard{Root: root, StateDir: state, Folder: "obsidian", Controller: &fakeController{}}
	if err := g.snapshotLocal("note.md"); err != nil {
		t.Fatal(err)
	}
	if err := g.detectRemoteOverwrite(context.Background(), "note.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g.snapshotPath("note.md")); !os.IsNotExist(err) {
		t.Fatalf("snapshot still exists or stat failed: %v", err)
	}
}

func mustContain(t *testing.T, path, want string) {
	t.Helper()
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs), want) {
		t.Fatalf("%s missing %q in %q", path, want, string(bs))
	}
}
