//go:build !unix

package main

import "os/exec"

func ownGroup(*exec.Cmd) {}

func stopGroup(cmd *exec.Cmd, _ bool) { cmd.Process.Kill() }
