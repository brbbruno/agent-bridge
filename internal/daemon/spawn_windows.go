//go:build windows

package daemon

import (
	"io"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func spawnDetached(executable string, args []string, env []string, home string) error {
	command := exec.Command(executable, args...)
	command.Dir = home
	command.Env = env
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
