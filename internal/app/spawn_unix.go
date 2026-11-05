//go:build !windows

package app

import (
	"os"
	"os/exec"
	"syscall"
)

// startDetached starts `a2dlc check --background` in dir in its own session, so it outlives the hook that started it,
// with its output in logPath.
func startDetached(dir, logPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	c := exec.Command(exe, "check", "--background")
	c.Dir, c.Stdout, c.Stderr = dir, log, log
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}
