package events

import (
	"context"
	"fmt"
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
