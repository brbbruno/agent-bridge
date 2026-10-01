//go:build windows

package autostart

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestAutostartEnableGetDisable(t *testing.T) {
	registryKey := fmt.Sprintf(`Software\agent-bridge-test-%d`, time.Now().UnixNano())
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, registryKey) })
	options := Options{Executable: `C:\Program Files\agent bridge\agent-bridge.exe`, RegistryKey: registryKey}
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
	if !status.Enabled || !status.MatchesExecutable || status.Command != `"C:\Program Files\agent bridge\agent-bridge.exe" daemon start` {
		t.Fatalf("enabled status=%+v", status)
	}
	options.Executable = `C:\New Path\agent-bridge.exe`
	status, err = Enable(options)
	if err != nil || !status.Enabled || !status.MatchesExecutable || !strings.Contains(status.Command, `C:\New Path`) {
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
