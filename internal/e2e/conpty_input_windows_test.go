//go:build windows

package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConPTYAcceptsInput(t *testing.T) {
	windowsRoot := os.Getenv("WINDIR")
	if windowsRoot == "" {
		windowsRoot = `C:\Windows`
	}
	commandPrompt := filepath.Join(windowsRoot, "System32", "cmd.exe")
	runner, err := startDevinPTY(commandPrompt, nil, os.Environ(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	time.Sleep(500 * time.Millisecond)
	if err := runner.SendLine("exit"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.done:
	case <-time.After(5 * time.Second):
		t.Fatal("ConPTY não encaminhou a entrada para o processo")
	}
}
