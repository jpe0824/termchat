package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"

	"github.com/jpe0824/termchat/internal/paths"
)

func TestConfigDefaults(t *testing.T) {
	t.Setenv("TERMCHAT_CONFIG_DIR", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.CLI.Send != "allow" || !c.MarkRead || !c.Splash || c.Notify.OSC != "auto" || !c.Notify.Terminal || c.Notify.OS != OSFallback {
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
	c.Notify.OS = OSOff
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
	dir, err := paths.Config()
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "config.toml"))
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

func TestLoadFailsWhenConfigDirUncreatable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERMCHAT_CONFIG_DIR", filepath.Join(blocker, "sub"))
	if _, err := paths.Config(); err == nil {
		t.Fatal("paths.Config() returned nil error")
	}
	if c, err := Load(); err == nil {
		t.Fatalf("Load() = %+v, want error", c)
	}
}

func TestPasteGapSetting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMCHAT_CONFIG_DIR", dir)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PasteGapMS != 25 {
		t.Errorf("default paste_gap_ms = %d, want 25", c.PasteGapMS)
	}
	for _, v := range []int{0, 80} {
		body := []byte("paste_gap_ms = " + strconv.Itoa(v) + "\n")
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		if c, err = Load(); err != nil || c.PasteGapMS != v {
			t.Errorf("paste_gap_ms = %d in the file: loaded %d, %v", v, c.PasteGapMS, err)
		}
	}
}

func TestPasteGapClamped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMCHAT_CONFIG_DIR", dir)
	for in, want := range map[int]int{5000: 200, 201: 200, -5: 0, 200: 200} {
		body := []byte("paste_gap_ms = " + strconv.Itoa(in) + "\n")
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		if c, err := Load(); err != nil || c.PasteGapMS != want {
			t.Errorf("paste_gap_ms = %d: loaded %d, %v; want %d", in, c.PasteGapMS, err, want)
		}
	}
}
