package hook

import (
	"testing"

	"github.com/brbbruno/agent-bridge/internal/config"
)

func TestParseDevinListTitle(t *testing.T) {
	data := fixture(t, "devin-list.json")
	if got := parseDevinListTitle(data, "possible-celestite"); got != "Corrigir login" {
		t.Fatalf("título=%q", got)
	}
	if got := parseDevinListTitle(data, "other"); got != "Outra tarefa" {
		t.Fatalf("título por short_id=%q", got)
	}
	if got := parseDevinListTitle(data, "unknown"); got != "" {
		t.Fatalf("título de sessão desconhecida=%q", got)
	}
	if got := parseDevinListTitle([]byte("invalid json"), "possible-celestite"); got != "" {
		t.Fatalf("título para JSON inválido=%q", got)
	}
	if got := parseDevinListTitle([]byte(`[{"id":"untitled","title":" Untitled "}]`), "untitled"); got != "" {
		t.Fatalf("título Untitled=%q", got)
	}
}

func TestDevinExecutableUsesConfiguredPath(t *testing.T) {
	cfg := config.Config{DevinExe: "X:/custom/devin.exe"}
	if got := devinExecutable(cfg); got != cfg.DevinExe {
		t.Fatalf("executável=%q", got)
	}
}
