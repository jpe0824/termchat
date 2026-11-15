//go:build windows

package ipc

import (
	"context"
	"errors"
)

func startProcess(context.Context) error {
	return errors.New("the background process is not supported on Windows")
}
