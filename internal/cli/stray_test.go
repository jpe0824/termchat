package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// startFakeDaemon runs a shell script installed as <dir>/b/termchat with the
// arguments "daemon run", so a process listing shows it as a person's
// background process, and stops it when the test ends.
func startFakeDaemon(t *testing.T, dir string) int {
	t.Helper()
	bin := filepath.Join(dir, "b", "termchat")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nwhile :; do sleep 60; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "daemon", "run")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

func TestStrayCheckSeesOnlyThisRun(t *testing.T) {
	skipOnWindows(t)
	// Another run of this package, laid out as now and as before run roots.
	other, err := os.MkdirTemp("/tmp", "tcr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(other) })
	older, err := os.MkdirTemp("/tmp", "tc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(older) })
	mineDir, err := os.MkdirTemp(runRoot, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(mineDir) })

	foreign := []int{startFakeDaemon(t, filepath.Join(other, "p1")), startFakeDaemon(t, older)}
	mine := startFakeDaemon(t, mineDir)
	// The script's shell forks before each sleep, and the child carries the
	// parent's command line until it execs, so a listing can briefly show it.
	eventually(t, fmt.Sprintf("strayDaemons() to list only %d, not the other runs' %v", mine, foreign),
		func() bool { return slices.Equal(strayDaemons(), []int{mine}) })
}
