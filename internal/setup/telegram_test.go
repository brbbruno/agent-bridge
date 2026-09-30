package setup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brbbruno/agent-bridge/internal/config"
)

type scriptedTerminal struct {
	password []byte
	answers  []string
	line     int
}

func (s *scriptedTerminal) IsTerminal() bool { return true }
func (s *scriptedTerminal) ReadPassword() ([]byte, error) {
	return append([]byte(nil), s.password...), nil
}
func (s *scriptedTerminal) ReadLine() (string, error) {
	if s.line >= len(s.answers) {
		return "", io.EOF
	}
	answer := s.answers[s.line]
	s.line++
	return answer, nil
}

func TestTelegramSetupOnlyAcceptsAndConfirmsPrivateStart(t *testing.T) {
	var sentChats []int64
	updates := []map[string]any{
		setupMessage(1, -100, "supergroup", "/start", "Grupo", ""),
		setupMessage(2, 88, "private", "/hello", "Visitante", "visitor"),
		setupMessage(3, 99, "private", "/startfoo", "Visitante", "visitor"),
		setupMessage(4, 101, "private", "/start", "Alice", "alice"),
		setupMessage(5, 202, "private", "/start", "Bruna", "bruna"),
		setupMessage(6, 303, "private", "/start", "Cecília", "cecilia"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		switch method {
		case "getMe":
			writeSetupJSON(w, map[string]any{"ok": true, "result": map[string]any{"id": 10, "is_bot": true, "first_name": "Bridge", "username": "bridge_bot"}})
		case "getUpdates":
			var request struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			result := make([]map[string]any, 0)
			for _, update := range updates {
				if update["update_id"].(int64) >= request.Offset {
					result = append(result, update)
				}
			}
			writeSetupJSON(w, map[string]any{"ok": true, "result": result})
		case "sendMessage":
			var request struct {
				ChatID int64 `json:"chat_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			sentChats = append(sentChats, request.ChatID)
			writeSetupJSON(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 50, "chat": map[string]any{"id": request.ChatID, "type": "private"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	cfg := config.Default()
	cfg.Telegram.APIBase = server.URL
	terminal := &scriptedTerminal{password: []byte("fake-token"), answers: []string{"n\n", "\n", "s\n"}}
	var output strings.Builder
	got, err := telegramWithTerminal(context.Background(), home, cfg, &output, terminal)
	if err != nil {
		t.Fatal(err)
	}
	if got.Telegram.ChatID != 303 || got.Telegram.BotToken != "fake-token" {
		t.Fatalf("configuração capturada incorreta: %+v", got.Telegram)
	}
	if terminal.line != 3 {
		t.Fatalf("confirmações lidas=%d, esperava 2", terminal.line)
	}
	for _, value := range []string{"Alice (@alice)", "chat_id=101", "Cecília (@cecilia)", "chat_id=303", "Confirmar este chat? [s/N]"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("prompt de confirmação não contém %q: %s", value, output.String())
		}
	}
	if len(sentChats) != 1 || sentChats[0] != 303 {
		t.Fatalf("confirmação enviada aos chats incorretos: %v", sentChats)
	}
	saved, err := config.Load(home)
	if err != nil || saved.Telegram.ChatID != 303 {
		t.Fatalf("configuração não salva: %+v, err=%v", saved.Telegram, err)
	}
}

func TestTelegramSetupRejectsNonTerminalWithMinttyHint(t *testing.T) {
	terminal := &nonTerminal{}
	_, err := telegramWithTerminal(context.Background(), t.TempDir(), config.Default(), io.Discard, terminal)
	if err == nil {
		t.Fatal("setup deve exigir terminal para ocultar token")
	}
	for _, hint := range []string{"PowerShell", "Windows Terminal", "Terminal.app", "mintty", "winpty agent-bridge setup telegram"} {
		if !strings.Contains(err.Error(), hint) {
			t.Fatalf("erro sem instrução %q: %v", hint, err)
		}
	}
}

type nonTerminal struct{}

func (nonTerminal) IsTerminal() bool              { return false }
func (nonTerminal) ReadPassword() ([]byte, error) { return nil, io.EOF }
func (nonTerminal) ReadLine() (string, error)     { return "", io.EOF }

func setupMessage(id, chatID int64, chatType, text, firstName, username string) map[string]any {
	return map[string]any{"update_id": id, "message": map[string]any{
		"message_id": id + 100,
		"text":       text,
		"chat":       map[string]any{"id": chatID, "type": chatType},
		"from":       map[string]any{"id": chatID, "first_name": firstName, "username": username},
	}}
}

func writeSetupJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
