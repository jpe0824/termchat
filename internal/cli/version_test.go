package cli

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	info := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	tests := []struct {
		name    string
		ldflag  string
		info    *debug.BuildInfo
		hasInfo bool
		want    string
	}{
		{"ldflag wins over build info", "v1.2.3", info("v9.9.9"), true, "v1.2.3"},
		{"build info when untagged", "dev", info("v0.4.0"), true, "v0.4.0"},
		{"build info of a pseudo-version", "dev", info("v0.0.0-20261114120000-abcdef123456"), true, "v0.0.0-20261114120000-abcdef123456"},
		{"devel build info is not a version", "dev", info("(devel)"), true, "dev"},
		{"empty build info version", "dev", info(""), true, "dev"},
		{"no build info", "dev", nil, false, "dev"},
		{"empty ldflag", "", nil, false, "dev"},
	}
	for _, tt := range tests {
		if got := resolveVersion(tt.ldflag, tt.info, tt.hasInfo); got != tt.want {
			t.Errorf("%s: resolveVersion = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// --version reports Version, whatever it was set to by -ldflags or build info.
func TestVersionFlagPrintsVersion(t *testing.T) {
	var out bytes.Buffer
	cmd := Root()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); !strings.HasSuffix(got, " "+Version) {
		t.Fatalf("--version printed %q, want it to end in %q", got, " "+Version)
	}
}
