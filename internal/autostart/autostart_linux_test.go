//go:build linux

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutostartEnableGetDisable(t *testing.T) {
	options := Options{Executable: filepath.Join(t.TempDir(), "Program Files", "agent-bridge"), ConfigDir: t.TempDir()}
	status, err := Get(options)
	if err != nil || status.Enabled {
		t.Fatalf("initial status=%+v, err=%v", status, err)
	}
	if err := Disable(options); err != nil {
		t.Fatalf("disable absent: %v", err)
	}
	status, err = Enable(options)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || !status.MatchesExecutable || status.Command != `"`+options.Executable+`" daemon start` {
		t.Fatalf("enabled status=%+v", status)
	}
	data, err := os.ReadFile(status.Location)
	if err != nil || !strings.Contains(string(data), "Terminal=false") || !strings.Contains(string(data), "NoDisplay=true") {
		t.Fatalf("desktop entry invalid: %s, err=%v", data, err)
	}
	info, err := os.Stat(status.Location)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("desktop entry mode=%v, err=%v", info, err)
	}
	options.Executable = filepath.Join(t.TempDir(), "New Path", "agent-bridge")
	status, err = Enable(options)
	if err != nil || !status.Enabled || !status.MatchesExecutable || !strings.Contains(status.Command, "New Path") {
		t.Fatalf("re-enabled status=%+v, err=%v", status, err)
	}
	if err := Disable(options); err != nil {
		t.Fatal(err)
	}
	status, err = Get(options)
	if err != nil || status.Enabled {
		t.Fatalf("disabled status=%+v, err=%v", status, err)
	}
	if err := Disable(options); err != nil {
		t.Fatalf("disable absent after delete: %v", err)
	}
}
