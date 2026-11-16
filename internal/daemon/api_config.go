package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
	"github.com/jpe0824/termchat/internal/store"
)

const accountKey = "account"

// ConfigGet implements ipc.Backend. Per-room entries are listed only when an
// account is available; without one (not joined, or the account failed to
// come up) it lists the global settings alone, deliberately, so the CLI can
// still read and change them.
func (d *daemon) ConfigGet(context.Context) ([]ipc.Setting, error) {
	cfg := d.config()
	out := make([]ipc.Setting, 0, len(config.Specs)+len(cfg.Accounts)+4)
	for _, sp := range config.Specs {
		out = append(out, settingOf(cfg, sp, ""))
	}
	if a, err := d.account(); err == nil {
		views, err := a.st.Conversations()
		if err != nil {
			return nil, err
		}
		views = slices.DeleteFunc(views, func(v store.ConvView) bool { return v.Kind != model.ConvRoom })
		slices.SortStableFunc(views, func(x, y store.ConvView) int { return strings.Compare(x.Name, y.Name) })
		for _, v := range views {
			sp, _ := config.SpecFor(config.RoomPrefix + v.ID)
			out = append(out, settingOf(cfg, sp, sanitize.Line(v.Name)))
		}
	}
	for _, ac := range cfg.Accounts {
		out = append(out, ipc.Setting{
			Key:     accountKey,
			Value:   ac.Handle + "@" + ac.Server,
			Applies: config.AppliesReadOnly,
			Help:    "change with termchat join",
		})
	}
	return out, nil
}

func settingOf(cfg *config.Config, sp config.Spec, room string) ipc.Setting {
	v, _ := cfg.Get(sp.Key)
	return ipc.Setting{Key: sp.Key, Value: v, Choices: sp.Choices, Applies: sp.Applies, Help: sp.Help, Room: room}
}

// ConfigSet implements ipc.Backend.
func (d *daemon) ConfigSet(_ context.Context, key, value, source string) (ipc.Setting, error) {
	switch source {
	case "cli", "tui":
	default:
		return ipc.Setting{}, fmt.Errorf("%w: unknown source %q", ipc.ErrInvalid, source)
	}
	if key == accountKey {
		return ipc.Setting{}, fmt.Errorf("%w: the account cannot be changed here; use termchat join", ipc.ErrInvalid)
	}
	sp, ok := config.SpecFor(key)
	if !ok {
		return ipc.Setting{}, fmt.Errorf("%w: %s: %w", ipc.ErrInvalid, key, config.ErrUnknownKey)
	}
	if err := config.Validate(key, value); err != nil {
		return ipc.Setting{}, fmt.Errorf("%w: %w", ipc.ErrInvalid, err)
	}
	room, err := d.checkRoomKey(key)
	if err != nil {
		return ipc.Setting{}, err
	}
	if source == "cli" && (key == "notify.hook" || (key == "cli.send" && value == "allow")) {
		return ipc.Setting{}, ipc.ErrRefused
	}
	cfg, err := d.updateConfig(func(c *config.Config) error { return c.Set(key, value) })
	if err != nil {
		return ipc.Setting{}, err
	}
	d.srv.Broadcast(ipc.Event{Kind: ipc.KindConfig})
	return settingOf(cfg, sp, room), nil
}

// checkRoomKey returns the sanitized room name of a rooms.<id> key, and
// fails unless the id is a room the account is in. Other keys pass.
func (d *daemon) checkRoomKey(key string) (string, error) {
	id, ok := strings.CutPrefix(key, config.RoomPrefix)
	if !ok {
		return "", nil
	}
	a, err := d.account()
	if err != nil {
		return "", err
	}
	v, found := a.view(id)
	switch {
	case !found:
		return "", fmt.Errorf("%w: conversation %q", ipc.ErrNotFound, id)
	case v.Kind != model.ConvRoom:
		return "", fmt.Errorf("%w: notification modes apply to rooms only", ipc.ErrInvalid)
	}
	return sanitize.Line(v.Name), nil
}
