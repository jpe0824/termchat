package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPathsHonourEnvOverride(t *testing.T) {
	cases := []struct {
		env string
		fn  func() (string, error)
	}{
		{"TERMCHAT_CONFIG_DIR", Config},
		{"TERMCHAT_DATA_DIR", Data},
		{"TERMCHAT_RUNTIME_DIR", Runtime},
	}
	for _, c := range cases {
		want := filepath.Join(t.TempDir(), "d")
		t.Setenv(c.env, want)
		got, err := c.fn()
		if err != nil {
			t.Fatalf("%s: %v", c.env, err)
		}
		if got != want {
			t.Fatalf("%s: got %q want %q", c.env, got, want)
		}
		st, err := os.Stat(got)
		if err != nil {
			t.Fatalf("%s: %v", c.env, err)
		}
		if !st.IsDir() {
			t.Fatalf("%s: not a directory", c.env)
		}
		if runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
			t.Fatalf("%s: mode %v, want 0700", c.env, st.Mode().Perm())
		}
	}
}

func TestRuntimeDefaultsUnderData(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("TERMCHAT_DATA_DIR", data)
	t.Setenv("TERMCHAT_RUNTIME_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	got, err := Runtime()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(data, "run"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestUncreatableDirIsAnError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERMCHAT_CONFIG_DIR", filepath.Join(blocker, "sub"))
	if got, err := Config(); err == nil {
		t.Fatalf("Config() = %q, want error", got)
	}
}
