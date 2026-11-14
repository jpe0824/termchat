//go:build windows

package notify

import (
	"os/exec"
	"time"
)

func shell(hook string) (string, []string) { return "cmd", []string{"/C", hook} }

// killTree bounds how long Wait lingers after the context ends. Children of
// the command are not tracked on Windows.
func killTree(cmd *exec.Cmd) { cmd.WaitDelay = time.Second }
