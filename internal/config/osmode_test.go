package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadNotify loads a config.toml whose [notify] section holds line.
func loadNotify(t *testing.T, line string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TERMCHAT_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[notify]\n"+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load()
}

func TestOSModeDecodes(t *testing.T) {
	for in, want := range map[string]OSMode{
		"os = true":     OSFallback,
		"os = false":    OSOff,
		`os = "always"`: OSAlways,
		`osc = "9"`:     OSFallback, // os unset keeps the default
	} {
		c, err := loadNotify(t, in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if c.Notify.OS != want {
			t.Errorf("%s: OS = %v, want %v", in, c.Notify.OS, want)
		}
	}
}

func TestOSModeRejectsOtherValues(t *testing.T) {
	for _, in := range []string{`os = "Always"`, `os = "fallback"`, `os = "true"`, `os = 1`, `os = ""`} {
		c, err := loadNotify(t, in)
		if err == nil {
			t.Errorf("%s: loaded %v, want an error", in, c.Notify.OS)
			continue
		}
		if !strings.Contains(err.Error(), "notify.os") {
			t.Errorf("%s: error %q does not name notify.os", in, err)
		}
	}
}

func TestOSModeRoundTripsThroughSave(t *testing.T) {
	for m, line := range map[OSMode]string{OSOff: "os = false", OSFallback: "os = true", OSAlways: `os = "always"`} {
		dir := t.TempDir()
		t.Setenv("TERMCHAT_CONFIG_DIR", dir)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		c.Notify.OS = m
		if err := c.Save(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), line) {
			t.Errorf("mode %v saved as:\n%s\nwant a line %q", m, raw, line)
		}
		got, err := Load()
		if err != nil {
			t.Fatalf("mode %v: reload: %v", m, err)
		}
		if got.Notify.OS != m {
			t.Errorf("mode %v reloaded as %v", m, got.Notify.OS)
		}
	}
}
