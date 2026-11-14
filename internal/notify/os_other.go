//go:build !darwin && !linux && !windows

package notify

// No operating-system route on this platform.
func platformCommand(func(string) (string, error), Notification) *Cmd { return nil }
