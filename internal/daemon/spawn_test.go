package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpawnDetachedUsesAgentBridgeHomeAsWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	probe := filepath.Join(t.TempDir(), "cwd.txt")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "AGENT_BRIDGE_SPAWN_CWD_PROBE="+probe)
	if err := spawnDetached(executable, []string{"-test.run=^TestSpawnHelperProcess$"}, environment, home); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(probe)
		if err == nil {
			if got := strings.TrimSpace(string(data)); resolvedPath(got) != resolvedPath(home) {
				t.Fatalf("daemon cwd=%q, want %q", got, home)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("processo destacado não gravou o diretório de trabalho")
}

func TestSpawnHelperProcess(t *testing.T) {
	probe := os.Getenv("AGENT_BRIDGE_SPAWN_CWD_PROBE")
	if probe == "" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probe, []byte(cwd), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}
