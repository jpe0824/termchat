//go:build windows

package daemon

import "errors"

func platformSupported() error {
	return errors.New("the termchat background process is not supported on Windows")
}

func lockFile(string) (func(), error) { return nil, platformSupported() }
