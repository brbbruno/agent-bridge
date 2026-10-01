//go:build !windows

package hook

import "os/exec"

func hideWindow(_ *exec.Cmd) {}
