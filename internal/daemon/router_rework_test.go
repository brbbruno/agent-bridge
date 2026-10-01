package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/channel/fake"
	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/logx"
	"github.com/brbbruno/agent-bridge/internal/model"
)

const reworkChatID int64 = 5050

func newReworkRouter(t *testing.T, wait time.Duration) (*Router, *fake.Fake) {
	t.Helper()
	cfg := config.Default()
	cfg.Telegram.ChatID = reworkChatID
	cfg.StopWait = wait
	cfg.PermissionWait = wait
	cfg.QuestionWait = wait
	bot := fake.New()
	router, err := NewRouter(t.TempDir(), cfg, bot, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	router.SetAway(true)
	return router, bot
}

func runReworkEvent(router *Router, event model.Event) <-chan model.Resolution {
	result := make(chan model.Resolution, 1)
	go func() { result <- router.HandleEvent(context.Background(), event) }()
	return result
}

func waitReworkSends(t *testing.T, bot *fake.Fake, count int) []fake.SentRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sent, _ := bot.Snapshot()
		if len(sent) >= count {
			return sent
		}
		time.Sleep(5 * time.Millisecond)
	}
	sent, _ := bot.Snapshot()
	t.Fatalf("esperava %d mensagens Telegram, recebi %d", count, len(sent))
	return nil
}

func assertReworkEditPreserves(t *testing.T, bot *fake.Fake, messageID int64, original, expected string) {
	t.Helper()
	_, edits := bot.Snapshot()
	for _, edit := range edits {
		if edit.ID == messageID && !edit.MarkupOnly && strings.Contains(edit.Text, original) && strings.Contains(edit.Text, expected) {
			return
		}
	}
	t.Fatalf("edição não preservou %q e adicionou %q: %+v", original, expected, edits)
}

func TestReworkStopResolutionKeepsOriginalText(t *testing.T) {
	router, bot := newReworkRouter(t, time.Second)
	event := model.Event{Agent: model.AgentDevin, Type: model.EventStop, SessionID: "stop", SessionName: "stop", Project: "demo", Message: "Resposta original do agente"}
	result := runReworkEvent(router, event)
	original := waitReworkSends(t, bot, 1)[0]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: reworkChatID, ReplyToMessage: original.ID, Text: "Continue"})
	if got := <-result; got.Action != model.ActionBlock {
		t.Fatalf("resolução=%+v", got)
	}
	assertReworkEditPreserves(t, bot, original.ID, original.Text, "Resposta recebida")
}

func TestReworkPermissionInstructionKeepsOriginalText(t *testing.T) {
	router, bot := newReworkRouter(t, time.Second)
	event := model.Event{Agent: model.AgentDevin, Type: model.EventPermission, SessionID: "permission", SessionName: "permission", Project: "demo", ToolName: "exec", ToolSummary: "python write_file.py"}
	result := runReworkEvent(router, event)
	original := waitReworkSends(t, bot, 1)[0]
	instruction := original.Keyboard[0][2]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: reworkChatID, CallbackID: "deny-instruction", CallbackData: instruction.Data})
	sent := waitReworkSends(t, bot, 2)
	forceReply := sent[1]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: reworkChatID, ReplyToMessage: forceReply.ID, Text: "Não execute"})
	if got := <-result; got.Action != model.ActionDeny {
		t.Fatalf("resolução=%+v", got)
	}
	assertReworkEditPreserves(t, bot, original.ID, original.Text, "Negado")
	assertReworkEditPreserves(t, bot, forceReply.ID, forceReply.Text, "Negado")
}

func TestReworkQuestionResolutionKeepsQuestionAndAnswer(t *testing.T) {
	router, bot := newReworkRouter(t, time.Second)
	event := model.Event{Agent: model.AgentClaude, Type: model.EventQuestion, SessionID: "question", SessionName: "question", Project: "demo", Questions: []model.Question{{Header: "Cor", Text: "Qual cor?", Options: []model.Option{{Label: "Azul"}, {Label: "Verde"}}}}}
	result := runReworkEvent(router, event)
	original := waitReworkSends(t, bot, 1)[0]
	green := original.Keyboard[1][0]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: reworkChatID, CallbackID: "green", CallbackData: green.Data})
	if got := <-result; got.Action != model.ActionBlock {
		t.Fatalf("resolução=%+v", got)
	}
	assertReworkEditPreserves(t, bot, original.ID, original.Text, "Resposta: Verde")
}

func TestReworkLongStopKeepsChunksAndRepliesWithStatus(t *testing.T) {
	router, bot := newReworkRouter(t, time.Second)
	event := model.Event{Agent: model.AgentDevin, Type: model.EventStop, SessionID: "long-stop", SessionName: "long-stop", Project: "demo"}
	prefix := router.header(event) + "\n"
	event.Message = strings.Repeat("x", 3*channel.MaxMessageRunes-40-len([]rune(prefix)))
	result := runReworkEvent(router, event)
	originals := waitReworkSends(t, bot, 3)
	last := originals[2]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: reworkChatID, ReplyToMessage: originals[0].ID, Text: "Continue"})
	if got := <-result; got.Action != model.ActionBlock {
		t.Fatalf("resolução=%+v", got)
	}
	sent, edits := bot.Snapshot()
	if len(sent) != 4 || sent[3].ReplyTo != last.ID || !strings.Contains(sent[3].Text, "Resposta recebida") {
		t.Fatalf("status não respondeu ao último segmento: %+v", sent)
	}
	for _, edit := range edits {
		if edit.ID == last.ID {
			t.Fatalf("segmento original foi editado apesar do limite: %+v", edit)
		}
	}
}

func TestReworkLongPermissionRemovesMarkupAndRepliesWithStatus(t *testing.T) {
	router, bot := newReworkRouter(t, time.Second)
	event := model.Event{Agent: model.AgentDevin, Type: model.EventPermission, SessionID: "long-permission", SessionName: "long-permission", Project: "demo", ToolName: "exec"}
	prefix := router.header(event) + "\nPrecisa de aprovação: " + event.ToolName + "\n"
	event.ToolSummary = strings.Repeat("x", 3*channel.MaxMessageRunes-30-len([]rune(prefix)))
	result := runReworkEvent(router, event)
	originals := waitReworkSends(t, bot, 3)
	last := originals[2]
	if len(last.Keyboard) == 0 {
		t.Fatal("último segmento da permissão deveria conter o teclado")
	}
	instruction := last.Keyboard[0][2]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: reworkChatID, CallbackID: "deny-instruction", CallbackData: instruction.Data})
	sent := waitReworkSends(t, bot, 4)
	forceReply := sent[3]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: reworkChatID, ReplyToMessage: forceReply.ID, Text: "Não execute"})
	if got := <-result; got.Action != model.ActionDeny {
		t.Fatalf("resolução=%+v", got)
	}
	allSent, edits := bot.Snapshot()
	if len(allSent) != 5 || allSent[4].ReplyTo != last.ID || !strings.Contains(allSent[4].Text, "Negado pelo usuário") {
		t.Fatalf("status não respondeu ao último segmento: %+v", allSent)
	}
	markupRemoved := false
	for _, edit := range edits {
		if edit.ID == last.ID {
			if !edit.MarkupOnly || edit.Text != "" {
				t.Fatalf("texto original foi substituído no overflow: %+v", edit)
			}
			markupRemoved = true
		}
	}
	if !markupRemoved {
		t.Fatal("teclado do último segmento não foi removido")
	}
	assertReworkEditPreserves(t, bot, forceReply.ID, forceReply.Text, "Negado")
}

func TestPruneLateMessageRegistryAndSessionLabels(t *testing.T) {
	router, _ := newReworkRouter(t, time.Second)
	now := time.Now()
	router.mu.Lock()
	router.sessionLabels["late-label"] = "late-session"
	router.sessionLabels["waiting-label"] = "waiting-session"
	router.sessionLabels["queued-label"] = "queued-session"
	router.sessionLabels["stale-label"] = "stale-session"
	router.waiting["waiting-session"] = "pending-id"
	router.state.Queues["queued-session"] = []string{"resposta"}
	for id := int64(1); id <= lateMessageLimit+3; id++ {
		router.lateMessage[id] = lateMessageEntry{sessionID: "late-session", createdAt: now.Add(time.Duration(id) * time.Second)}
	}
	router.pruneLateMessagesLocked(now)
	if len(router.lateMessage) != lateMessageLimit {
		router.mu.Unlock()
		t.Fatalf("lateMessage=%d, esperava %d", len(router.lateMessage), lateMessageLimit)
	}
	for id := int64(1); id <= 3; id++ {
		if _, ok := router.lateMessage[id]; ok {
			router.mu.Unlock()
			t.Fatalf("entrada antiga %d não foi removida", id)
		}
	}
	if _, ok := router.sessionLabels["stale-label"]; ok {
		router.mu.Unlock()
		t.Fatal("label inativo permaneceu")
	}
	for id, entry := range router.lateMessage {
		entry.createdAt = now.Add(-25 * time.Hour)
		router.lateMessage[id] = entry
	}
	router.pruneLateMessagesLocked(now)
	if len(router.lateMessage) != 0 {
		router.mu.Unlock()
		t.Fatalf("mensagens expiradas permaneceram: %d", len(router.lateMessage))
	}
	if _, ok := router.sessionLabels["late-label"]; ok {
		router.mu.Unlock()
		t.Fatal("label sem atividade permaneceu após a expiração")
	}
	if _, ok := router.sessionLabels["waiting-label"]; !ok {
		router.mu.Unlock()
		t.Fatal("label de sessão aguardando foi removido")
	}
	if _, ok := router.sessionLabels["queued-label"]; !ok {
		router.mu.Unlock()
		t.Fatal("label com fila foi removido")
	}
	delete(router.waiting, "waiting-session")
	delete(router.state.Queues, "queued-session")
	router.pruneSessionLabelsLocked()
	if len(router.sessionLabels) != 0 {
		router.mu.Unlock()
		t.Fatalf("labels inativos permaneceram: %v", router.sessionLabels)
	}
	router.mu.Unlock()
}
