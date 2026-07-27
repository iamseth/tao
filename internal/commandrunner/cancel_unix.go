//go:build unix

package commandrunner

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCommandCancellation isolates the command in a process group so
// cancellation and command completion terminate descendants it left behind.
func configureCommandCancellation(cmd *exec.Cmd) func() {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	killGroup := func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = killGroup
	return func() { _ = killGroup() }
}
