//go:build !windows

package notify

import (
	"os/exec"
	"syscall"
	"time"
)

func shell(hook string) (string, []string) { return "sh", []string{"-c", hook} }

// killTree puts cmd in its own process group and, when its context ends,
// kills the whole group so a hook's children do not outlive it.
func killTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
}
