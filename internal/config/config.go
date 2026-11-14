// Package config loads and saves the user's config.toml.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/jpe0824/termchat/internal/paths"
)

// Account is one server login. Server is "host:port".
type Account struct {
	Name   string `toml:"name"`
	Server string `toml:"server"`
	Handle string `toml:"handle"`
}

type Config struct {
	Accounts []Account `toml:"account"`
	CLI      struct {
		Send string `toml:"send"` // "allow" | "deny"
	} `toml:"cli"`
	Notify struct {
		Terminal bool   `toml:"terminal"`
		OS       bool   `toml:"os"`
		OSC      string `toml:"osc"` // "auto" | "9" | "99" | "777" | "off"
		Hook     string `toml:"hook"`
	} `toml:"notify"`
	Rooms    map[string]string `toml:"rooms"` // conversation ID -> "all" | "mentions" | "muted"
	MarkRead bool              `toml:"mark_read"`
	Splash   bool              `toml:"splash"`
}

func defaults() *Config {
	c := &Config{MarkRead: true, Splash: true}
	c.CLI.Send = "allow"
	c.Notify.Terminal = true
	c.Notify.OS = true
	c.Notify.OSC = "auto"
	return c
}

func file() (string, error) {
	dir, err := paths.Config()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// Load reads config.toml, filling unset keys with defaults. A missing file
// yields the defaults.
func Load() (*Config, error) {
	c := defaults()
	f, err := file()
	if err != nil {
		return nil, err
	}
	_, err = toml.DecodeFile(f, c)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return c, nil
}

// Save writes config.toml with mode 0600 via a temp file and rename, so a
// crash never leaves a truncated file.
func (c *Config) Save() error {
	dest, err := file()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := toml.NewEncoder(tmp).Encode(c); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}
