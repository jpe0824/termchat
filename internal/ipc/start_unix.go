//go:build !windows

package ipc

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// startProcess runs "<this binary> daemon run" in its own session, so it
// outlives the caller and receives no signals from the caller's terminal.
func startProcess(context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "daemon", "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it if it exits while the caller runs
	return nil
}
