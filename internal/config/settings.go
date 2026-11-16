package config

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Applies says when a changed setting takes effect.
type Applies string

const (
	// AppliesProcess: the background process re-reads it at once.
	AppliesProcess Applies = "process"
	// AppliesTUI: the running interface picks it up at once.
	AppliesTUI Applies = "tui"
	// AppliesLaunch: it is read only when the interface starts.
	AppliesLaunch Applies = "launch"
	// AppliesReadOnly: it cannot be changed through Set.
	AppliesReadOnly Applies = "read-only"
)

// Spec describes one setting.
type Spec struct {
	Key string
	// Choices lists the allowed values; nil means free text or an integer.
	Choices []string
	Applies Applies
	Help    string
}

// RoomPrefix starts the key of a per-room notification mode: rooms.<id>.
const RoomPrefix = "rooms."

var (
	boolChoices = []string{"true", "false"}
	roomChoices = []string{"all", "mentions", "muted"}
)

var (
	ErrUnknownKey = errors.New("unknown setting")
	ErrBadValue   = errors.New("bad value")
)

// Specs lists the settings Set accepts, in display order. Per-room modes
// (RoomPrefix) are not listed; SpecFor describes them.
var Specs = []Spec{
	{"notify.terminal", boolChoices, AppliesProcess, "Show notifications inside the terminal."},
	{"notify.os", []string{"false", "true", "always"}, AppliesProcess, "Post through the operating system: never, only when no interface showed it, or always."},
	{"notify.osc", []string{"auto", "9", "99", "777", "7501", "off"}, AppliesTUI, "Terminal escape sequence used for notifications."},
	{"notify.hook", nil, AppliesProcess, "Command run for each notification; empty for none."},
	{"mark_read", boolChoices, AppliesTUI, "Mark a conversation read when the interface shows it."},
	{"paste_gap_ms", nil, AppliesTUI, "Milliseconds to wait after enter for more pasted input, 0 to 200."},
	{"splash", boolChoices, AppliesLaunch, "Show the splash screen when the interface starts."},
	{"cli.send", []string{"allow", "deny"}, AppliesProcess, "Whether CLI commands may send messages."},
}

// SpecFor returns the spec for key, including a synthetic one for any
// rooms.<id> key.
func SpecFor(key string) (Spec, bool) {
	if strings.HasPrefix(key, RoomPrefix) {
		return Spec{key, roomChoices, AppliesProcess, "Notification mode for this room."}, true
	}
	for _, s := range Specs {
		if s.Key == key {
			return s, true
		}
	}
	return Spec{}, false
}

type settingError struct {
	msg  string
	kind error
}

func (e *settingError) Error() string { return e.msg }
func (e *settingError) Unwrap() error { return e.kind }

func badChoice(key, value string, choices []string) error {
	return &settingError{
		fmt.Sprintf("%s: %q is not one of %s", key, value, strings.Join(choices, ", ")),
		ErrBadValue,
	}
}

func parseBool(key, value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, badChoice(key, value, boolChoices)
}

// Get returns the value of key as text, in the form Set accepts. An unset
// rooms.<id> is "mentions".
func (c *Config) Get(key string) (string, error) {
	if _, ok := SpecFor(key); !ok {
		return "", &settingError{key + ": unknown setting", ErrUnknownKey}
	}
	if id, ok := strings.CutPrefix(key, RoomPrefix); ok {
		if m := c.Rooms[id]; m != "" {
			return m, nil
		}
		return "mentions", nil
	}
	switch key {
	case "notify.terminal":
		return strconv.FormatBool(c.Notify.Terminal), nil
	case "notify.os":
		return c.Notify.OS.String(), nil
	case "notify.osc":
		return c.Notify.OSC, nil
	case "notify.hook":
		return c.Notify.Hook, nil
	case "mark_read":
		return strconv.FormatBool(c.MarkRead), nil
	case "paste_gap_ms":
		return strconv.Itoa(c.PasteGapMS), nil
	case "splash":
		return strconv.FormatBool(c.Splash), nil
	case "cli.send":
		return c.CLI.Send, nil
	}
	return "", &settingError{key + ": unknown setting", ErrUnknownKey}
}

// Set parses and validates value and stores it under key. Setting a room to
// "mentions" removes its entry, since that is the default. paste_gap_ms is
// clamped to 0..maxPasteGapMS.
func (c *Config) Set(key, value string) error {
	spec, ok := SpecFor(key)
	if !ok {
		return &settingError{key + ": unknown setting", ErrUnknownKey}
	}
	if spec.Choices != nil && !slices.Contains(spec.Choices, value) {
		return badChoice(key, value, spec.Choices)
	}
	if id, ok := strings.CutPrefix(key, RoomPrefix); ok {
		if value == "mentions" {
			delete(c.Rooms, id)
			return nil
		}
		if c.Rooms == nil {
			c.Rooms = map[string]string{}
		}
		c.Rooms[id] = value
		return nil
	}
	switch key {
	case "notify.terminal", "mark_read", "splash":
		b, err := parseBool(key, value)
		if err != nil {
			return err
		}
		switch key {
		case "notify.terminal":
			c.Notify.Terminal = b
		case "mark_read":
			c.MarkRead = b
		default:
			c.Splash = b
		}
	case "notify.os":
		c.Notify.OS = map[string]OSMode{"false": OSOff, "true": OSFallback, "always": OSAlways}[value]
	case "notify.osc":
		c.Notify.OSC = value
	case "notify.hook":
		c.Notify.Hook = value
	case "paste_gap_ms":
		n, err := strconv.Atoi(value)
		if err != nil {
			return &settingError{fmt.Sprintf("%s: %q is not a whole number", key, value), ErrBadValue}
		}
		c.PasteGapMS = min(max(n, 0), maxPasteGapMS)
	case "cli.send":
		c.CLI.Send = value
	}
	return nil
}

// Validate reports whether Set would accept key and value, without a
// configuration to apply them to.
func Validate(key, value string) error {
	return defaults().Set(key, value)
}
