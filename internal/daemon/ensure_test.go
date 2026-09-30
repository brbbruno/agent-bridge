package daemon

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/brbbruno/agent-bridge/internal/config"
)

func TestDaemonEnvironmentStripsACPBackendAndPinsHome(t *testing.T) {
	environment := daemonEnvironment([]string{"PATH=C:/bin", "ACP_BACKEND=desktop", "AGENT_BRIDGE_HOME=C:/old", "VSCODE_PID=1"}, "C:/bridge home")
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "ACP_BACKEND=") {
		t.Fatal("daemon não deve herdar ACP_BACKEND")
	}
	if !strings.Contains(joined, "AGENT_BRIDGE_HOME=C:/bridge home") || !strings.Contains(joined, "VSCODE_PID=1") {
		t.Fatalf("ambiente inesperado: %s", joined)
	}
}

func TestLoadOrCreateTokenUsesPrivateMode(t *testing.T) {
	home := t.TempDir()
	first, err := LoadOrCreateToken(home)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateToken(home)
	if err != nil || first != second || len(first) < 40 {
		t.Fatalf("token instável ou curto; err=%v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(config.TokenPath(home))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("permissões do token: %o", info.Mode().Perm())
		}
	}
}

func TestAuthorizedRejectsWrongToken(t *testing.T) {
	if !authorized("same", "same") || authorized("wrong", "same") {
		t.Fatal("validação de token incorreta")
	}
}
