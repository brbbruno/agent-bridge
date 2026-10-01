package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/channel/fake"
	"github.com/brbbruno/agent-bridge/internal/model"
)

func TestStopResolutionKeepsOriginalMessage(t *testing.T) {
	router, bot, _ := newTestRouter(t, time.Second)
	result := runEvent(router, event(model.EventStop, "preserve-stop"))
	sent := waitForSent(t, bot, 1)[0]
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, ReplyToMessage: sent.ID, Text: "Continue"})
	if got := <-result; got.Action != model.ActionBlock {
		t.Fatalf("resolução=%+v", got)
	}
	assertPendingEdit(t, bot, sent.ID, sent.Text, "Resposta recebida")
}

func TestQuestionResolutionKeepsQuestionAndAnswer(t *testing.T) {
	router, bot, _ := newTestRouter(t, time.Second)
	question := event(model.EventQuestion, "preserve-question")
	question.Questions = []model.Question{{Text: "Qual cor?", Options: []model.Option{{Label: "Azul"}, {Label: "Verde"}}}}
	result := runEvent(router, question)
	sent := waitForSent(t, bot, 1)[0]
	button := sent.Keyboard[1][0]
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, CallbackID: "answer-green", CallbackData: button.Data})
	if got := <-result; got.Action != model.ActionBlock {
		t.Fatalf("resolução=%+v", got)
	}
	assertPendingEdit(t, bot, sent.ID, sent.Text, "Resposta: Verde")
}

func TestLongStopUsesReplyStatusWithoutReplacingChunks(t *testing.T) {
	router, bot, _ := newTestRouter(t, time.Second)
	value := event(model.EventStop, "long-stop")
	prefix := router.header(value) + "\n"
	value.Message = strings.Repeat("x", 3*channel.MaxMessageRunes-40-len([]rune(prefix)))
	result := runEvent(router, value)
	sent := waitForSent(t, bot, 3)
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, ReplyToMessage: sent[0].ID, Text: "Continue"})
	if got := <-result; got.Action != model.ActionBlock {
		t.Fatalf("resolução=%+v", got)
	}
	allSent, edits := bot.Snapshot()
	if len(allSent) != 4 || allSent[3].ReplyTo != sent[2].ID || !strings.Contains(allSent[3].Text, "Resposta recebida") {
		t.Fatalf("status não respondeu ao último segmento: %+v", allSent)
	}
	for _, edit := range edits {
		for _, original := range sent {
			if edit.ID == original.ID {
				t.Fatalf("mensagem original foi editada no overflow: %+v", edit)
			}
		}
	}
}

func TestLongPermissionInstructionKeepsTextAndRemovesOnlyMarkup(t *testing.T) {
	router, bot, _ := newTestRouter(t, time.Second)
	value := event(model.EventPermission, "long-permission")
	prefix := router.header(value) + "\nPrecisa de aprovação: " + value.ToolName + "\n"
	value.ToolSummary = strings.Repeat("x", 3*channel.MaxMessageRunes-30-len([]rune(prefix)))
	result := runEvent(router, value)
	sent := waitForSent(t, bot, 3)
	lastOriginal := sent[2]
	instructionButton := lastOriginal.Keyboard[0][2]
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, CallbackID: "deny-instruct", CallbackData: instructionButton.Data})
	sent = waitForSent(t, bot, 4)
	if !sent[3].ForceReply {
		t.Fatal("instrução de negação não usou ForceReply")
	}
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, ReplyToMessage: sent[3].ID, Text: "Não execute"})
	if got := <-result; got.Action != model.ActionDeny {
		t.Fatalf("resolução=%+v", got)
	}
	allSent, edits := bot.Snapshot()
	if len(allSent) != 5 || allSent[4].ReplyTo != lastOriginal.ID || !strings.Contains(allSent[4].Text, "Negado pelo usuário") {
		t.Fatalf("status não respondeu ao último segmento: %+v", allSent)
	}
	markupRemoved := false
	for _, edit := range edits {
		if edit.ID != lastOriginal.ID {
			continue
		}
		if !edit.MarkupOnly || edit.Text != "" {
			t.Fatalf("o texto original foi substituído no overflow: %+v", edit)
		}
		markupRemoved = true
	}
	if !markupRemoved {
		t.Fatal("markup do último segmento não foi removido")
	}
	assertPendingEdit(t, bot, sent[3].ID, sent[3].Text, "Negado")
}

func assertPendingEdit(t *testing.T, bot *fake.Fake, messageID int64, original, expected string) {
	t.Helper()
	_, edits := bot.Snapshot()
	for _, edit := range edits {
		if edit.ID == messageID && !edit.MarkupOnly && strings.Contains(edit.Text, original) && strings.Contains(edit.Text, expected) {
			return
		}
	}
	t.Fatalf("edição não preservou %q e adicionou %q: %+v", original, expected, edits)
}
