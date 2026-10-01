package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brbbruno/agent-bridge/internal/config"
)

func TestDiscordSetupSavesTokenAndPrintsInvite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot fake-discord-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/users/@me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "bot-id", "username": "bridge-bot"})
		case "/oauth2/applications/@me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "app-id", "flags": int64(1 << 19)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	terminal := &scriptedTerminal{password: []byte("fake-discord-token")}
	var output strings.Builder
	got, err := discordWithTerminal(context.Background(), home, config.Default(), &output, terminal, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.Discord.BotToken != "fake-discord-token" {
		t.Fatalf("token não foi guardado na config retornada: %+v", got.Discord)
	}
	loaded, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Discord.BotToken != "fake-discord-token" {
		t.Fatalf("token Discord não foi salvo: %+v", loaded.Discord)
	}
	if !strings.Contains(output.String(), "https://discord.com/oauth2/authorize?client_id=app-id&scope=bot%20applications.commands&permissions=326417583104") {
		t.Fatalf("URL de convite ausente: %s", output.String())
	}
	if strings.Contains(output.String(), "fake-discord-token") {
		t.Fatal("token apareceu na saída")
	}
}

func TestDiscordSetupWarnsWhenMessageContentIntentIsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/@me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "bot-id", "username": "bridge-bot"})
		case "/oauth2/applications/@me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "app-id", "flags": int64(0)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	terminal := &scriptedTerminal{password: []byte("fake-discord-token")}
	var output strings.Builder
	if _, err := discordWithTerminal(context.Background(), t.TempDir(), config.Default(), &output, terminal, server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Message Content Intent") || !strings.Contains(output.String(), "Developer Portal") {
		t.Fatalf("aviso de intent ausente: %s", output.String())
	}
}

func TestDiscordSetupRejectsNonTerminal(t *testing.T) {
	_, err := discordWithTerminal(context.Background(), t.TempDir(), config.Default(), &strings.Builder{}, &nonTerminal{}, nil, discordAPIBase)
	if err == nil || !strings.Contains(err.Error(), "PowerShell") || !strings.Contains(err.Error(), "ocultar") {
		t.Fatalf("erro de terminal não interativo=%v", err)
	}
}
