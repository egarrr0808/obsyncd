package events

import (
	"context"
	"errors"
	"testing"
	"time"

	stevents "github.com/syncthing/syncthing/lib/events"
	"github.com/thejerf/suture/v4"
)

type fakeHandler struct {
	paths []string
	err   error
}

func (f *fakeHandler) HandleArtifact(_ context.Context, path string) error {
	f.paths = append(f.paths, path)
	return f.err
}

func TestLoopHandlesConflictEvents(t *testing.T) {
	logger, cancel := startLogger(t)
	defer cancel()

	handler := &fakeHandler{}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	done := make(chan error, 1)
	go func() {
		done <- Loop{Logger: logger, Folder: "vault", Handler: handler}.Run(ctx)
	}()
	time.Sleep(50 * time.Millisecond)

	logger.Log(stevents.ItemFinished, map[string]interface{}{
		"folder": "vault",
		"item":   "vault/note.sync-conflict-20260704-120000-REMOTE.md",
		"type":   "file",
	})

	deadline := time.After(2 * time.Second)
	for len(handler.paths) == 0 {
		select {
		case <-deadline:
			t.Fatal("handler not called")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("loop error = %v", err)
	}
}

func TestFileEventPath(t *testing.T) {
	path, folder, ok := fileEventPath(stevents.Event{
		Type: stevents.LocalChangeDetected,
		Data: map[string]interface{}{
			"folder": "vault",
			"path":   "note.md",
			"type":   "file",
		},
	})
	if !ok || path != "note.md" || folder != "vault" {
		t.Fatalf("path=%q folder=%q ok=%v", path, folder, ok)
	}
	if _, _, ok := fileEventPath(stevents.Event{
		Type: stevents.ItemFinished,
		Data: map[string]interface{}{"item": "dir", "type": "dir"},
	}); ok {
		t.Fatal("dir event should be ignored")
	}
}

func startLogger(t *testing.T) (stevents.Logger, context.CancelFunc) {
	t.Helper()
	logger := stevents.NewLogger()
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := suture.New("test", suture.Spec{})
	supervisor.Add(logger)
	errs := supervisor.ServeBackground(ctx)
	go func() {
		if err := <-errs; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("logger stopped: %v", err)
		}
	}()
	return logger, cancel
}
