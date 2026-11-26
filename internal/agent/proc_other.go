//go:build !unix

package agent

import (
	"os"
	"os/exec"
	"syscall"
)

// Process groups are a Unix concept; elsewhere only the leader is killed.

func setProcessGroup(*exec.Cmd) {}

func signalGroup(pid int, _ syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func groupAlive(int) bool { return false }
