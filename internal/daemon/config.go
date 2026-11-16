package daemon

import (
	"fmt"

	"github.com/jpe0824/termchat/internal/config"
)

// config returns the current settings. Any goroutine may call it, the drain
// goroutine included: it never blocks. The result is shared and must not be
// changed; updateConfig replaces it instead.
func (d *daemon) config() *config.Config {
	return d.live.Load()
}

// updateConfig applies change to config.toml as it is on disk now, so edits
// made to the file by hand since the process started are kept, saves it and
// makes it the current settings. On any error the file and the current
// settings are left as they were and the error is returned unwrapped.
//
// d.mu may be held by the caller; nothing may take d.mu while holding
// d.cfgMu.
func (d *daemon) updateConfig(change func(*config.Config) error) (*config.Config, error) {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	c, err := d.opts.LoadConfig()
	if err != nil {
		return nil, err
	}
	if err := change(c); err != nil {
		return nil, err
	}
	if err := d.opts.SaveConfig(c); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	d.live.Store(c)
	return c, nil
}
