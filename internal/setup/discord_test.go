package setup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/brbbruno/agent-bridge/internal/config"
)

const authorizedDiscordID = "123456789012345678"

func discordSetupTestServer(t *testing.T, flags int64, userStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot fake-discord-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/users/@me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "bot-id", "username": "bridge-bot"})
		case "/oauth2/applications/@me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "app-id", "flags": flags})
		case "/users/" + authorizedDiscordID:
			if userStatus != http.StatusOK {
				http.Error(w, "unknown user", userStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": authorizedDiscordID, "username": "alice"})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestDiscordSetupSavesTokenAndAuthorizedUser(t *testing.T) {
	server := discordSetupTestServer(t, 1<<19, http.StatusOK)
	defer server.Close()
	home := t.TempDir()
	cfg := config.Default()
	cfg.Discord.AllowedUserIDs = []string{"old-authorized-user"}
	terminal := &scriptedTerminal{password: []byte("fake-discord-token"), answers: []string{authorizedDiscordID}}
	var output strings.Builder
	got, err := discordWithTerminal(context.Background(), home, cfg, &output, terminal, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.Discord.BotToken != "fake-discord-token" || len(got.Discord.AllowedUserIDs) != 1 || got.Discord.AllowedUserIDs[0] != authorizedDiscordID {
		t.Fatalf("credenciais não foram guardadas na config retornada: %+v", got.Discord)
	}
	loaded, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Discord.BotToken != "fake-discord-token" || len(loaded.Discord.AllowedUserIDs) != 1 || loaded.Discord.AllowedUserIDs[0] != authorizedDiscordID {
		t.Fatalf("token ou usuário autorizado não foi salvo: %+v", loaded.Discord)
	}
	if !strings.Contains(output.String(), "Usuário autorizado: alice") || !strings.Contains(output.String(), "https://discord.com/oauth2/authorize?client_id=app-id&scope=bot%20applications.commands&permissions=326417583104") {
		t.Fatalf("confirmação ou URL de convite ausente: %s", output.String())
	}
	if strings.Contains(output.String(), "fake-discord-token") {
		t.Fatal("token apareceu na saída")
	}
}

func TestDiscordSetupRejectsInvalidUserIDWithoutSaving(t *testing.T) {
	server := discordSetupTestServer(t, 1<<19, http.StatusOK)
	defer server.Close()
	home := t.TempDir()
	terminal := &scriptedTerminal{password: []byte("fake-discord-token"), answers: []string{"123abc"}}
	_, err := discordWithTerminal(context.Background(), home, config.Default(), &strings.Builder{}, terminal, server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "17 a 20 dígitos") {
		t.Fatalf("ID inválido não foi rejeitado: %v", err)
	}
	if _, err := os.Stat(config.ConfigPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configuração foi salva para ID inválido: %v", err)
	}
}

func TestDiscordSetupRejectsUnknownUserWithoutSaving(t *testing.T) {
	server := discordSetupTestServer(t, 1<<19, http.StatusNotFound)
	defer server.Close()
	home := t.TempDir()
	terminal := &scriptedTerminal{password: []byte("fake-discord-token"), answers: []string{authorizedDiscordID}}
	_, err := discordWithTerminal(context.Background(), home, config.Default(), &strings.Builder{}, terminal, server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "Discord HTTP 404") {
		t.Fatalf("usuário inexistente não foi rejeitado: %v", err)
	}
	if _, err := os.Stat(config.ConfigPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configuração foi salva para usuário inexistente: %v", err)
	}
}

func TestDiscordSetupWarnsWhenMessageContentIntentIsMissing(t *testing.T) {
	server := discordSetupTestServer(t, 0, http.StatusOK)
	defer server.Close()
	terminal := &scriptedTerminal{password: []byte("fake-discord-token"), answers: []string{authorizedDiscordID}}
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
