//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, so that stopping it
// stops what its shell started too.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// stopGroup sends SIGTERM to cmd's process group, or SIGKILL.
func stopGroup(cmd *exec.Cmd, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	syscall.Kill(-cmd.Process.Pid, sig)
}
