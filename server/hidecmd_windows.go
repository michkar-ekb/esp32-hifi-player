//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hiddenCmd starts a helper (ffmpeg) without flashing a console window.
func hiddenCmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return c
}
