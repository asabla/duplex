//go:build unix

package source

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Resolver helpers belong to the same process group. Canceling abandoned work
// must also stop its JavaScript child, before another source job starts.
func configureResolverProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}

		return err
	}
}
