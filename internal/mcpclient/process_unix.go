//go:build unix

package mcpclient

import (
	"errors"
	"os/exec"
	"syscall"
)

// Keep wrappers and their children in a separate group. The SDK closes stdin,
// then escalates to SIGTERM/SIGKILL and Wait for the direct process. Afterwards
// remove descendants that retained the group, including on failed startup.
func isolateProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}
func cleanupProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
