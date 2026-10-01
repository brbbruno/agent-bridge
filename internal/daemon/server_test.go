package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/channel/fake"
	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/logx"
	"github.com/brbbruno/agent-bridge/internal/model"
)

func TestHTTPAuthAndEventEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.NotifyWhenPresent = false
	cfg.Telegram.ChatID = 7
	server, err := NewServer(t.TempDir(), cfg, "secret-test-token", []channel.Channel{fake.New()}, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	unauthorized, err := http.Get(httpServer.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sem token: HTTP %d", unauthorized.StatusCode)
	}
	client := NewClient(1, "secret-test-token")
	client.base = httpServer.URL
	client.SetHTTPClient(httpServer.Client())
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("health autenticado: %v", err)
	}
	result, err := client.Handle(context.Background(), model.Event{Agent: model.AgentDevin, Type: model.EventStop, SessionID: "session", Project: "demo", Message: "pronto"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != model.ActionNone {
		t.Fatalf("away off deve ser não bloqueante: %+v", result)
	}
}

func TestAdminTestBroadcastsToAllConfiguredChannels(t *testing.T) {
	telegram := fake.New()
	discord := fake.NewNamed("Discord", 2000)
	server, err := NewServer(t.TempDir(), config.Default(), "test-token", []channel.Channel{telegram, discord}, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/admin/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tokenHeader, "test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP=%d", resp.StatusCode)
	}
	telegramSent, _ := telegram.Snapshot()
	discordSent, _ := discord.Snapshot()
	if len(telegramSent) != 1 || !strings.Contains(telegramSent[0].Text, "Telegram funcionando") {
		t.Fatalf("mensagem de teste Telegram=%+v", telegramSent)
	}
	if len(discordSent) != 1 || !strings.Contains(discordSent[0].Text, "Discord funcionando") {
		t.Fatalf("mensagem de teste Discord=%+v", discordSent)
	}
}

func TestAdminTestWithoutChannelsReturnsUnavailable(t *testing.T) {
	server, err := NewServer(t.TempDir(), config.Default(), "test-token", nil, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/admin/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tokenHeader, "test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("HTTP=%d; esperava 503", resp.StatusCode)
	}
}

func TestHTTPAcceptsProgressEvent(t *testing.T) {
	server, err := NewServer(t.TempDir(), config.Default(), "auth-token", nil, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	body, _ := json.Marshal(model.Event{Agent: model.AgentDevin, Type: model.EventProgress, SessionID: "session", TurnID: "turn", ToolName: "exec"})
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/progress", bytes.NewReader(body))
	req.Header.Set(tokenHeader, "auth-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP=%d; esperava 200", resp.StatusCode)
	}
	var resolution model.Resolution
	if err := json.NewDecoder(resp.Body).Decode(&resolution); err != nil || resolution.Action != model.ActionNone {
		t.Fatalf("resolução progress=%+v err=%v", resolution, err)
	}
}

func TestHTTPRejectsMismatchedEventType(t *testing.T) {
	cfg := config.Default()
	server, err := NewServer(t.TempDir(), cfg, "auth-token", nil, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	body, _ := json.Marshal(model.Event{Agent: model.AgentDevin, Type: model.EventPermission, SessionID: "s"})
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/stop", bytes.NewReader(body))
	req.Header.Set(tokenHeader, "auth-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("HTTP=%d; esperava 400", resp.StatusCode)
	}
}
