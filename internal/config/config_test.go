package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/jpe0824/termchat/internal/paths"
)

func TestConfigDefaults(t *testing.T) {
	t.Setenv("TERMCHAT_CONFIG_DIR", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.CLI.Send != "allow" || !c.MarkRead || !c.Splash || c.Notify.OSC != "auto" || !c.Notify.Terminal || !c.Notify.OS {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	t.Setenv("TERMCHAT_CONFIG_DIR", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	c.Accounts = []Account{{Name: "home", Server: "chat.example.com:7777", Handle: "jason"}}
	c.CLI.Send = "deny"
	c.Notify.OS = false
	c.Notify.OSC = "777"
	c.Notify.Hook = "/usr/local/bin/notify"
	c.Rooms = map[string]string{"abc": "muted", "def": "mentions"}
	c.MarkRead = false
	c.Splash = false
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, got) {
		t.Fatalf("round trip mismatch:\n saved  %+v\n loaded %+v", c, got)
	}
	st, err := os.Stat(filepath.Join(paths.Config(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", st.Mode().Perm())
	}
}

func TestConfigPartialFileKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMCHAT_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("splash = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Splash || !c.MarkRead || c.CLI.Send != "allow" {
		t.Fatalf("unexpected: %+v", c)
	}
}
