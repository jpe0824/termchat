package cli

import (
	"bytes"
	"strings"
	"testing"
)

// An untagged build reports "dev"; release builds replace Version with
// -ldflags "-X github.com/jpe0824/termchat/internal/cli.Version=...".
func TestVersionDefaultsToDev(t *testing.T) {
	if Version != "dev" {
		t.Fatalf("Version = %q, want %q", Version, "dev")
	}
	var out bytes.Buffer
	cmd := Root()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); !strings.HasSuffix(got, " dev") {
		t.Fatalf("--version printed %q, want it to end in %q", got, " dev")
	}
}
