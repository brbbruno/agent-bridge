//go:build windows

package hook

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func hideWindow(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
