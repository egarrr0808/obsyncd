package interceptor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obsyncd/internal/statestore"
)

type fakeController struct {
	paused  bool
	resumed bool
	rescans []string
}

func (f *fakeController) Pause(context.Context, string) error {
	f.paused = true
	return nil
}

func (f *fakeController) Resume(context.Context, string) error {
	f.resumed = true
	return nil
}

func (f *fakeController) Rescan(_ context.Context, _ string, paths []string) error {
	f.rescans = append(f.rescans, paths...)
	return nil
}

type fakeBases map[string]string

func (f fakeBases) Base(_ context.Context, _, path string) (string, bool, error) {
	v, ok := f[path]
	return v, ok, nil
}

func (f fakeBases) SaveBase(context.Context, string, string, string) error { return nil }

func (f fakeBases) Stage(context.Context, string, string, string) (statestore.Pending, error) {
	return statestore.Pending{}, nil
}

func TestCanonicalPath(t *testing.T) {
	got, ok := CanonicalPath("dir/note.sync-conflict-20260704-120000-ABCDEF.md")
	if !ok {
		t.Fatal("expected conflict path")
	}
	if got != filepath.Join("dir", "note.md") {
		t.Fatalf("canonical = %q", got)
	}
	if _, ok := CanonicalPath("../note.sync-conflict-20260704-120000-ABCDEF.md"); ok {
		t.Fatal("expected unsafe path rejection")
	}
}

func TestHandleArtifactMergesAndDeletesConflict(t *testing.T) {
	root := t.TempDir()
	canonicalRel := filepath.Join("vault", "note.md")
	artifactRel := filepath.Join("vault", "note.sync-conflict-20260704-120000-REMOTE.md")
	mustWrite(t, filepath.Join(root, canonicalRel), "a\nlocal\nc\n")
	mustWrite(t, filepath.Join(root, artifactRel), "a\nremote\nc\n")

	ctrl := &fakeController{}
	store := statestore.New(root)
	if err := store.SaveBase(context.Background(), "vault", canonicalRel, "a\nb\nc\n"); err != nil {
		t.Fatal(err)
	}
	in := &Interceptor{
		Root:       root,
		Folder:     "vault",
		Controller: ctrl,
		Bases:      store,
	}
	if err := in.HandleArtifact(context.Background(), artifactRel); err != nil {
		t.Fatal(err)
	}

	merged := string(mustRead(t, filepath.Join(root, canonicalRel)))
	if strings.Contains(merged, "%%OBSYNCD_CONFLICT_START%%") || merged != "a\nlocal\nc\n" {
		t.Fatalf("canonical changed during staged conflict: %q", merged)
	}
	if _, err := os.Stat(filepath.Join(root, artifactRel)); !os.IsNotExist(err) {
		t.Fatalf("artifact still exists or stat failed: %v", err)
	}
	pending, err := store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Canonical != filepath.ToSlash(canonicalRel) {
		t.Fatalf("pending = %#v", pending)
	}
	if !ctrl.paused || !ctrl.resumed {
		t.Fatalf("pause/resume not called: %#v", ctrl)
	}
	if len(ctrl.rescans) != 0 {
		t.Fatalf("rescans = %#v", ctrl.rescans)
	}
}

func TestHandleArtifactAutoMergesNonOverlapping(t *testing.T) {
	root := t.TempDir()
	canonicalRel := filepath.Join("vault", "note.md")
	artifactRel := filepath.Join("vault", "note.sync-conflict-20260704-120000-REMOTE.md")
	mustWrite(t, filepath.Join(root, canonicalRel), "title local\nbody\nend\n")
	mustWrite(t, filepath.Join(root, artifactRel), "title\nbody\nend remote\n")

	ctrl := &fakeController{}
	store := statestore.New(root)
	if err := store.SaveBase(context.Background(), "vault", canonicalRel, "title\nbody\nend\n"); err != nil {
		t.Fatal(err)
	}
	in := &Interceptor{
		Root:       root,
		Folder:     "vault",
		Controller: ctrl,
		Bases:      store,
	}
	if err := in.HandleArtifact(context.Background(), artifactRel); err != nil {
		t.Fatal(err)
	}

	merged := string(mustRead(t, filepath.Join(root, canonicalRel)))
	if strings.Contains(merged, "%%OBSYNCD_CONFLICT_START%%") {
		t.Fatalf("unexpected conflict markers in %q", merged)
	}
	want := "title local\nbody\nend remote\n"
	if merged != want {
		t.Fatalf("merged = %q want %q", merged, want)
	}
	if _, err := os.Stat(filepath.Join(root, artifactRel)); !os.IsNotExist(err) {
		t.Fatalf("artifact still exists or stat failed: %v", err)
	}
}

func TestHandleArtifactKeepsArtifactWhenCanonicalMissing(t *testing.T) {
	root := t.TempDir()
	artifactRel := "note.sync-conflict-20260704-120000-REMOTE.md"
	mustWrite(t, filepath.Join(root, artifactRel), "remote\n")

	in := &Interceptor{
		Root:       root,
		Folder:     "vault",
		Controller: &fakeController{},
		Bases:      fakeBases{},
	}
	if err := in.HandleArtifact(context.Background(), artifactRel); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(filepath.Join(root, artifactRel)); err != nil {
		t.Fatalf("artifact should remain: %v", err)
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

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
