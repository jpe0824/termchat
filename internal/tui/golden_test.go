package tui

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden frames in testdata")

// TestGoldenFrames compares whole colourless frames of the fixture, one per
// layout mode, so any change to the layout shows up as a diff.
func TestGoldenFrames(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"wide", 112, 16},
		{"standard", 84, 14},
		{"narrow", 44, 14},
		{"tiny", 30, 14},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := started(t, fixture(), c.w, c.h)
			got := h.view() + "\n"
			path := filepath.Join("testdata", c.name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("%s frame differs from %s:\n--- got\n%s--- want\n%s", c.name, path, got, want)
			}
		})
	}
}

func TestGoldenNarrowList(t *testing.T) {
	h := started(t, fixture(), 44, 14)
	h.key("esc")
	got := h.view() + "\n"
	path := filepath.Join("testdata", "narrow-list.golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("narrow list differs:\n--- got\n%s--- want\n%s", got, want)
	}
}

// TestGoldenSlashList is the command list under a lone / in each layout.
func TestGoldenSlashList(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"wide", 112, 16},
		{"standard", 84, 14},
		{"narrow", 44, 14},
		{"tiny", 30, 14},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := opened(t, fixture(), c.w, c.h)
			h.key("/")
			if !cmdMode(h) {
				t.Fatal("/ alone did not enter command mode")
			}
			checkGolden(t, "slash-list-"+c.name, h.view()+"\n")
		})
	}
}

// checkGolden compares got with testdata/<name>.golden, rewriting it first
// under -update.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("frame differs from %s:\n--- got\n%s--- want\n%s", path, got, want)
	}
}
