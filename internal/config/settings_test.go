package config

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

var validValues = map[string]string{
	"notify.terminal": "false",
	"notify.os":       "always",
	"notify.osc":      "7501",
	"notify.hook":     "say hi",
	"mark_read":       "false",
	"paste_gap_ms":    "40",
	"splash":          "false",
	"cli.send":        "deny",
}

func TestSetGetRoundTrip(t *testing.T) {
	if len(Specs) != len(validValues) {
		t.Fatalf("Specs has %d keys, test covers %d", len(Specs), len(validValues))
	}
	for _, s := range Specs {
		want, ok := validValues[s.Key]
		if !ok {
			t.Fatalf("no test value for %s", s.Key)
		}
		c := defaults()
		if err := c.Set(s.Key, want); err != nil {
			t.Fatalf("Set(%s, %q): %v", s.Key, want, err)
		}
		got, err := c.Get(s.Key)
		if err != nil || got != want {
			t.Fatalf("Get(%s) = %q, %v; want %q", s.Key, got, err, want)
		}
	}
}

func TestSetRejects(t *testing.T) {
	c := defaults()
	err := c.Set("notify.os", "sometimes")
	if !errors.Is(err, ErrBadValue) {
		t.Fatalf("notify.os: %v", err)
	}
	if want := `notify.os: "sometimes" is not one of false, true, always`; err.Error() != want {
		t.Fatalf("message %q, want %q", err.Error(), want)
	}
	if err := c.Set("nope", "1"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("nope: %v", err)
	}
	if err := c.Set("mark_read", "yes"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("mark_read: %v", err)
	}
	if err := c.Set("paste_gap_ms", "x"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("paste_gap_ms: %v", err)
	}
	if err := c.Set("rooms.c1", "loud"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("rooms.c1: %v", err)
	}
	if _, err := c.Get("nope"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Get nope: %v", err)
	}
}

func TestSetClampsPasteGap(t *testing.T) {
	for in, want := range map[string]string{"500": "200", "-3": "0"} {
		c := defaults()
		if err := c.Set("paste_gap_ms", in); err != nil {
			t.Fatal(err)
		}
		if got, _ := c.Get("paste_gap_ms"); got != want {
			t.Fatalf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestRoomMentionsDeletes(t *testing.T) {
	c := defaults()
	if got, err := c.Get("rooms.c1"); err != nil || got != "mentions" {
		t.Fatalf("unset room = %q, %v", got, err)
	}
	if err := c.Set("rooms.c1", "all"); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get("rooms.c1"); got != "all" {
		t.Fatalf("got %q", got)
	}
	if err := c.Set("rooms.c1", "mentions"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Rooms["c1"]; ok {
		t.Fatal("mentions left an entry")
	}
	if got, _ := c.Get("rooms.c1"); got != "mentions" {
		t.Fatalf("got %q", got)
	}
}

func TestValidateAndSpecFor(t *testing.T) {
	if err := Validate("rooms.anything", "muted"); err != nil {
		t.Fatal(err)
	}
	if err := Validate("rooms.anything", "loud"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("got %v", err)
	}
	if err := Validate("cli.send", "maybe"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("got %v", err)
	}
	s, ok := SpecFor("rooms.c9")
	if !ok || s.Applies != AppliesProcess || !reflect.DeepEqual(s.Choices, []string{"all", "mentions", "muted"}) {
		t.Fatalf("rooms spec = %+v, %v", s, ok)
	}
	if s, ok := SpecFor("splash"); !ok || s.Key != "splash" {
		t.Fatalf("splash spec = %+v, %v", s, ok)
	}
	if _, ok := SpecFor("nope"); ok {
		t.Fatal("unknown key has a spec")
	}
}

func TestSaveRoundTripsSettings(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "tc-cfg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TERMCHAT_CONFIG_DIR", dir)

	c := defaults()
	for k, v := range validValues {
		if err := c.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Set("rooms.c1", "muted"); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("loaded %+v, want %+v", got, c)
	}
}
