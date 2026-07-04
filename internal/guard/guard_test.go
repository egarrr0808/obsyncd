package guard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obsyncd/internal/statestore"
)

type fakeController struct{ paths []string }

func (f *fakeController) Pause(context.Context, string) error { return nil }

func (f *fakeController) Rescan(_ context.Context, _ string, paths []string) error {
	f.paths = append(f.paths, paths...)
	return nil
}

type fakeStager struct {
	canonical string
	content   string
}

func (f *fakeStager) Stage(_ context.Context, _, canonicalRel, artifactPath string) (statestore.Pending, error) {
	bs, err := os.ReadFile(artifactPath)
	if err != nil {
		return statestore.Pending{}, err
	}
	f.canonical = canonicalRel
	f.content = string(bs)
	_ = os.Remove(artifactPath)
	return statestore.Pending{Canonical: canonicalRel, Staged: "staged"}, nil
}

func (f *fakeStager) HasPending(context.Context, string, string) (bool, error) {
	return f.canonical != "", nil
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
	if err := g.snapshotLocal(context.Background(), "note.md"); err != nil {
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
	if err := g.snapshotLocal(context.Background(), "note.md"); err != nil {
		t.Fatal(err)
	}
	if err := g.detectRemoteOverwrite(context.Background(), "note.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g.snapshotPath("note.md")); !os.IsNotExist(err) {
		t.Fatalf("snapshot still exists or stat failed: %v", err)
	}
}

func TestDetectRemoteOverwriteDropsStaleSnapshot(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	path := filepath.Join(root, "note.md")
	if err := os.WriteFile(path, []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &Guard{Root: root, StateDir: state, Folder: "obsidian", Controller: &fakeController{}, MaxSnapshotAge: time.Millisecond}
	if err := g.snapshotLocal(context.Background(), "note.md"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(g.snapshotPath("note.md"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("remote edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.detectRemoteOverwrite(context.Background(), "note.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g.snapshotPath("note.md")); !os.IsNotExist(err) {
		t.Fatalf("stale snapshot still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(path, ".md") + ".local-v1.md"); !os.IsNotExist(err) {
		t.Fatalf("unexpected conflict copy: %v", err)
	}
	mustContain(t, path, "remote edit\n")
}

func TestSnapshotScannerCapturesChangedMarkdown(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	path := filepath.Join(root, "note.md")
	if err := os.WriteFile(path, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &Guard{Root: root, StateDir: state, Folder: "obsidian", Controller: &fakeController{}}
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		errs <- g.RunSnapshotScanner(ctx, 10*time.Millisecond)
	}()
	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(path, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if bs, err := os.ReadFile(g.snapshotPath("note.md")); err == nil && string(bs) == "local\n" {
			cancel()
			<-errs
			return
		}
		if time.Now().After(deadline) {
			cancel()
			<-errs
			t.Fatal("snapshot not captured")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSnapshotScannerStagesSecondChange(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	path := filepath.Join(root, "note.md")
	if err := os.WriteFile(path, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeController{}
	stager := &fakeStager{}
	g := &Guard{Root: root, StateDir: state, Folder: "obsidian", Controller: ctrl, Stager: stager}
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		errs <- g.RunSnapshotScanner(ctx, 10*time.Millisecond)
	}()
	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(path, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(g.snapshotPath("note.md")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-errs
			t.Fatal("snapshot not captured")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(path, []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		if stager.content == "remote\n" {
			cancel()
			<-errs
			mustContain(t, path, "local\n")
			if len(ctrl.paths) != 1 || ctrl.paths[0] != "note.md" {
				t.Fatalf("rescans = %#v", ctrl.paths)
			}
			return
		}
		if time.Now().After(deadline) {
			cancel()
			<-errs
			t.Fatalf("not staged: %#v", stager)
		}
		time.Sleep(10 * time.Millisecond)
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
