package config

import "fmt"

// OSMode is notify.os: when the background process posts a notification
// through the operating system. It is written as true, false or "always" so
// that files from before "always" existed keep their meaning.
type OSMode int

const (
	// OSOff (false) never posts through the operating system.
	OSOff OSMode = iota
	// OSFallback (true) posts only when no attached interface showed the
	// notification in the terminal.
	OSFallback
	// OSAlways ("always") posts whether or not an interface showed it.
	OSAlways
)

// UnmarshalTOML accepts true, false and "always".
func (m *OSMode) UnmarshalTOML(v any) error {
	switch v {
	case true:
		*m = OSFallback
	case false:
		*m = OSOff
	case "always":
		*m = OSAlways
	default:
		return fmt.Errorf(`notify.os: %#v is not true, false or "always"`, v)
	}
	return nil
}

// MarshalTOML writes m in the form UnmarshalTOML reads.
func (m OSMode) MarshalTOML() ([]byte, error) {
	switch m {
	case OSOff:
		return []byte("false"), nil
	case OSFallback:
		return []byte("true"), nil
	case OSAlways:
		return []byte(`"always"`), nil
	}
	return nil, fmt.Errorf("notify.os: unknown mode %d", int(m))
}

func (m OSMode) String() string {
	switch m {
	case OSOff:
		return "false"
	case OSFallback:
		return "true"
	case OSAlways:
		return "always"
	}
	return fmt.Sprintf("OSMode(%d)", int(m))
}
