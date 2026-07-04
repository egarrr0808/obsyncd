package events

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obsyncd/internal/interceptor"

	stevents "github.com/syncthing/syncthing/lib/events"
)

type Handler interface {
	HandleArtifact(ctx context.Context, artifactRel string) error
}

type Loop struct {
	Logger  stevents.Logger
	Folder  string
	Handler Handler
}

func (l Loop) Run(ctx context.Context) error {
	if l.Logger == nil {
		return fmt.Errorf("event logger is nil")
	}
	if l.Handler == nil {
		return fmt.Errorf("handler is nil")
	}

	sub := l.Logger.Subscribe(stevents.ItemFinished | stevents.LocalChangeDetected)
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
			if !ok {
				continue
			}
			if l.Folder != "" && folder != "" && folder != l.Folder {
				continue
			}
			if _, ok := interceptor.CanonicalPath(path); !ok {
				continue
			}
			if err := l.Handler.HandleArtifact(ctx, path); err != nil {
				return err
			}
		}
	}
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

type EventSource struct {
	Logger stevents.Logger
	Mask   stevents.EventType
}

type BaseStore interface {
	SaveBase(ctx context.Context, folder, path, content string) error
}

type BaseCapture struct {
	Logger stevents.Logger
	Folder string
	Root   string
	Store  BaseStore
}

func (c BaseCapture) Run(ctx context.Context) error {
	if c.Logger == nil {
		return fmt.Errorf("event logger is nil")
	}
	if c.Store == nil {
		return fmt.Errorf("base store is nil")
	}
	sub := c.Logger.Subscribe(stevents.ItemFinished)
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
			if !ok || !capturePath(path) {
				continue
			}
			if c.Folder != "" && folder != "" && folder != c.Folder {
				continue
			}
			full, err := safeJoin(c.Root, path)
			if err != nil {
				continue
			}
			bs, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			if err := c.Store.SaveBase(ctx, folder, path, string(bs)); err != nil {
				return err
			}
		}
	}
}

func (s EventSource) Events(ctx context.Context, since int64) ([]interceptor.Event, error) {
	if s.Logger == nil {
		return nil, fmt.Errorf("event logger is nil")
	}
	mask := s.Mask
	if mask == 0 {
		mask = stevents.ItemFinished | stevents.LocalChangeDetected
	}
	sub := s.Logger.Subscribe(mask)
	defer sub.Unsubscribe()

	for {
		ev, err := sub.Poll(time.Second)
		if err == nil {
			path, folder, ok := fileEventPath(ev)
			if !ok || int64(ev.GlobalID) <= since {
				continue
			}
			return []interceptor.Event{{
				ID:     int64(ev.GlobalID),
				Type:   ev.Type.String(),
				Folder: folder,
				Path:   path,
			}}, nil
		}
		if err == stevents.ErrTimeout {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		return nil, err
	}
}

func capturePath(path string) bool {
	slash := filepath.ToSlash(filepath.Clean(path))
	if strings.HasPrefix(slash, ".obsidian/obsyncd-") || strings.Contains(filepath.Base(slash), ".sync-conflict-") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}

func safeJoin(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe relative path: %s", rel)
	}
	return filepath.Join(root, clean), nil
}
