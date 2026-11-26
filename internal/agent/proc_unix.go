//go:build unix

package agent

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcessGroup makes the command the leader of a new process group, so a
// cancel can signal everything it started (hooks, docker, kubectl, ...).
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalGroup sends sig to every process in group pgid.
func signalGroup(pgid int, sig syscall.Signal) error {
	return syscall.Kill(-pgid, sig)
}

// groupAlive reports whether any process of group pgid still exists.
func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
