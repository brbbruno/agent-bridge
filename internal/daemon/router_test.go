package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
	fakechannel "github.com/brbbruno/agent-bridge/internal/channel/fake"
	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/logx"
	"github.com/brbbruno/agent-bridge/internal/model"
)

func newTestRouter(t *testing.T, wait time.Duration) (*Router, *fakechannel.Fake, string) {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default()
	cfg.StopWait = wait
	cfg.PermissionWait = wait
	cfg.QuestionWait = wait
	cfg.Telegram.ChatID = 123
	cfg.MachineName = "PC-TESTE"
	fake := fakechannel.New()
	router, err := NewRouter(home, cfg, fake, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	router.SetAway(true)
	return router, fake, home
}

func event(kind model.EventType, session string) model.Event {
	return model.Event{Agent: model.AgentDevin, Type: kind, SessionID: session, SessionName: session, Project: "demo", Message: "Turno concluído", ToolName: "exec", ToolSummary: "echo ok"}
}

func waitForSent(t *testing.T, fake *fakechannel.Fake, count int) []fakechannel.SentRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sent, _ := fake.Snapshot()
		if len(sent) >= count {
			return sent
		}
		time.Sleep(5 * time.Millisecond)
	}
	sent, _ := fake.Snapshot()
	t.Fatalf("esperava %d mensagens; recebeu %d", count, len(sent))
	return nil
}

func runEvent(router *Router, value model.Event) <-chan model.Resolution {
	result := make(chan model.Resolution, 1)
	go func() { result <- router.HandleEvent(context.Background(), value) }()
	return result
}

func TestReplyRoutesToPendingStop(t *testing.T) {
	router, fake, _ := newTestRouter(t, time.Second)
	result := runEvent(router, event(model.EventStop, "session-one"))
	sent := waitForSent(t, fake, 1)
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, Text: "Agora responda com BANANA", ReplyToMessage: sent[0].ID})
	got := <-result
	if got.Action != model.ActionBlock || !strings.Contains(got.Reason, "BANANA") {
		t.Fatalf("resolução=%+v", got)
	}
	if strings.Contains(sent[0].Text, "session-one") == false {
		t.Fatalf("cabeçalho não identifica sessão: %q", sent[0].Text)
	}
}

func TestSingleWaitingSessionAcceptsPlainReply(t *testing.T) {
	router, fake, _ := newTestRouter(t, time.Second)
	result := runEvent(router, event(model.EventStop, "only-session"))
	waitForSent(t, fake, 1)
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, Text: "CONTINUE"})
	got := <-result
	if got.Action != model.ActionBlock || !strings.Contains(got.Reason, "CONTINUE") {
		t.Fatalf("resolução=%+v", got)
	}
}

func TestMultipleWaitingSessionsShowPickerAndRouteReply(t *testing.T) {
	router, fake, _ := newTestRouter(t, 3*time.Second)
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()
	first := make(chan model.Resolution, 1)
	second := make(chan model.Resolution, 1)
	go func() { first <- router.HandleEvent(ctx1, event(model.EventStop, "session-a")) }()
	go func() { second <- router.HandleEvent(ctx2, event(model.EventStop, "session-b")) }()
	waitForSent(t, fake, 2)
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, Text: "Resposta roteada"})
	sent := waitForSent(t, fake, 3)
	if len(sent[2].Keyboard) != 2 || len(sent[2].Keyboard[0]) != 1 {
		t.Fatalf("picker inválido: %+v", sent[2].Keyboard)
	}
	selected := sent[2].Keyboard[0][0].Data
	if !strings.HasPrefix(selected, "r:") {
		t.Fatalf("callback inesperado: %s", selected)
	}
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, CallbackID: "cb-route", CallbackData: selected})
	sent = waitForSent(t, fake, 4)
	if !sent[3].ForceReply {
		t.Fatal("esperava ForceReply após escolher sessão")
	}
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, Text: "BANANA", ReplyToMessage: sent[3].ID})
	select {
	case got := <-first:
		if got.Action != model.ActionBlock || !strings.Contains(got.Reason, "BANANA") {
			t.Fatalf("primeira sessão: %+v", got)
		}
	case got := <-second:
		if got.Action != model.ActionBlock || !strings.Contains(got.Reason, "BANANA") {
			t.Fatalf("segunda sessão: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("sessão selecionada não foi resolvida")
	}
}

func TestLateReplyQueuedAndDeliveredAtNextStop(t *testing.T) {
	router, fake, home := newTestRouter(t, 70*time.Millisecond)
	result := runEvent(router, event(model.EventStop, "late-session"))
	sent := waitForSent(t, fake, 1)
	if got := <-result; got.Action != model.ActionNone {
		t.Fatalf("timeout deve deixar o agente encerrar: %+v", got)
	}
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, Text: "continue depois", ReplyToMessage: sent[0].ID})
	if persisted, err := loadState(home); err != nil || len(persisted.Queues["late-session"]) != 1 {
		t.Fatalf("fila não persistida: %+v, err=%v", persisted, err)
	}
	got := router.HandleEvent(context.Background(), event(model.EventStop, "late-session"))
	if got.Action != model.ActionBlock || !strings.Contains(got.Reason, "continue depois") {
		t.Fatalf("resposta tardia não foi entregue: %+v", got)
	}
	router.HandleEvent(context.Background(), model.Event{Type: model.EventSessionEnd, SessionID: "late-session"})
	if persisted, err := loadState(home); err != nil || len(persisted.Queues["late-session"]) != 0 {
		t.Fatalf("SessionEnd não limpou fila: %+v, err=%v", persisted, err)
	}
}

func TestPermissionDenyWithInstructionAndQuestionOther(t *testing.T) {
	router, fake, _ := newTestRouter(t, time.Second)
	permissionResult := runEvent(router, event(model.EventPermission, "permission-session"))
	sent := waitForSent(t, fake, 1)
	if !strings.Contains(sent[0].Text, "\n```\necho ok\n```") {
		t.Fatalf("resumo de permissão não está em bloco de código: %q", sent[0].Text)
	}
	instructionButton := sent[0].Keyboard[0][2]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, CallbackID: "cb-deny", CallbackData: instructionButton.Data})
	sent = waitForSent(t, fake, 2)
	if !sent[1].ForceReply {
		t.Fatal("esperava force reply para instrução de negação")
	}
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, Text: "Use a pasta temp", ReplyToMessage: sent[1].ID})
	permission := <-permissionResult
	if permission.Action != model.ActionDeny || !strings.Contains(permission.Reason, "Use a pasta temp") {
		t.Fatalf("permission=%+v", permission)
	}

	questionEvent := event(model.EventQuestion, "question-session")
	questionEvent.Questions = []model.Question{{Header: "Cor", Text: "Qual cor?", Options: []model.Option{{Label: "Azul"}, {Label: "Verde"}}}}
	questionResult := runEvent(router, questionEvent)
	sent = waitForSent(t, fake, 3)
	if !strings.Contains(sent[2].Text, "**Cor**\nQual cor?") {
		t.Fatalf("cabeçalho da pergunta não está em negrito: %q", sent[2].Text)
	}
	otherButton := sent[2].Keyboard[len(sent[2].Keyboard)-1][0]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, CallbackID: "cb-other", CallbackData: otherButton.Data})
	sent = waitForSent(t, fake, 4)
	if !sent[3].ForceReply {
		t.Fatal("Outro (texto) deve abrir ForceReply")
	}
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, Text: "Roxo", ReplyToMessage: sent[3].ID})
	question := <-questionResult
	if question.Action != model.ActionBlock || !strings.Contains(question.Reason, "Roxo") || !strings.Contains(question.Reason, "não chame a ferramenta") {
		t.Fatalf("question=%+v", question)
	}
}

func TestQuestionMultiSelectAndDisconnectCancellation(t *testing.T) {
	router, fake, _ := newTestRouter(t, time.Second)
	questionEvent := event(model.EventQuestion, "multi-session")
	questionEvent.Questions = []model.Question{{Header: "Frutas", Text: "Escolha", MultiSelect: true, Options: []model.Option{{Label: "Um"}, {Label: "Dois"}}}}
	result := runEvent(router, questionEvent)
	sent := waitForSent(t, fake, 1)
	firstButton := sent[0].Keyboard[0][0]
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, CallbackID: "cb-toggle", CallbackData: firstButton.Data})
	sent = waitForSent(t, fake, 1)
	_, edits := fake.Snapshot()
	if len(edits) == 0 || !strings.Contains(edits[0].Text, "**Frutas**\nEscolha") {
		t.Fatalf("edição da pergunta não reutilizou o texto formatado: %+v", edits)
	}
	confirm := sent[0].Keyboard[len(sent[0].Keyboard)-1][0]
	if confirm.Text != "Confirmar" {
		t.Fatalf("botão de confirmação ausente: %+v", sent[0].Keyboard)
	}
	router.HandleUpdate(context.Background(), channel.Update{ChatID: 123, CallbackID: "cb-confirm", CallbackData: confirm.Data})
	got := <-result
	if got.Action != model.ActionBlock || !strings.Contains(got.Reason, "Um") {
		t.Fatalf("resposta multi-select=%+v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelResult := make(chan model.Resolution, 1)
	go func() { cancelResult <- router.HandleEvent(ctx, event(model.EventStop, "disconnect-session")) }()
	waitForSent(t, fake, 2)
	cancel()
	if got := <-cancelResult; got.Action != model.ActionNone {
		t.Fatalf("disconnect deveria falhar aberto: %+v", got)
	}
	_, edits = fake.Snapshot()
	found := false
	for _, edit := range edits {
		if strings.Contains(strings.ToLower(edit.Text), "cancelado") {
			found = true
		}
	}
	if !found {
		t.Fatal("cancelamento do cliente não editou mensagem Telegram")
	}
}

func TestQuestionTimeoutBlocksWithPlainTextFallback(t *testing.T) {
	router, fake, _ := newTestRouter(t, 40*time.Millisecond)
	question := event(model.EventQuestion, "question-timeout")
	question.Questions = []model.Question{{Text: "Qual opção?", Options: []model.Option{{Label: "A"}}}}
	result := runEvent(router, question)
	waitForSent(t, fake, 1)
	got := <-result
	if got.Action != model.ActionBlock || !strings.Contains(got.Reason, "faça as perguntas em texto") {
		t.Fatalf("timeout de pergunta=%+v", got)
	}
}

func TestPermissionTimeoutFallsBackWithoutDecision(t *testing.T) {
	router, fake, _ := newTestRouter(t, 40*time.Millisecond)
	result := runEvent(router, event(model.EventPermission, "permission-timeout"))
	waitForSent(t, fake, 1)
	if got := <-result; got.Action != model.ActionNone {
		t.Fatalf("timeout de permissão deve voltar ao fluxo local: %+v", got)
	}
}

func TestHeaderShowsMachineAndSessionTitle(t *testing.T) {
	router, _, _ := newTestRouter(t, time.Second)
	value := model.Event{Agent: model.AgentDevin, Project: "demo", SessionName: "possible-celestite", SessionTitle: "Corrigir login"}
	want := "**Devin · PC-TESTE · demo**\nSessão: Corrigir login (possible-celestite)\n"
	if got := router.header(value); got != want {
		t.Fatalf("cabeçalho=%q; esperado %q", got, want)
	}
	value.SessionTitle = ""
	want = "**Devin · PC-TESTE · demo**\nSessão: possible-celestite\n"
	if got := router.header(value); got != want {
		t.Fatalf("cabeçalho sem título=%q; esperado %q", got, want)
	}
}

func TestHeaderFallsBackToFirstPrompt(t *testing.T) {
	router, fake, home := newTestRouter(t, time.Second)
	first := event(model.EventPrompt, "S")
	first.Prompt = "Primeiro   pedido\ncom quebra"
	if got := router.HandleEvent(context.Background(), first); got.Action != model.ActionContext {
		t.Fatalf("primeiro prompt=%+v", got)
	}
	second := event(model.EventPrompt, "S")
	second.Prompt = "Segundo"
	if got := router.HandleEvent(context.Background(), second); got.Action != model.ActionContext {
		t.Fatalf("segundo prompt=%+v", got)
	}
	stop := event(model.EventStop, "S")
	if got := router.header(stop); !strings.Contains(got, "Sessão: Primeiro pedido com quebra (") || strings.Contains(got, "Segundo") {
		t.Fatalf("título do primeiro prompt inesperado: %q", got)
	}

	reloaded, err := NewRouter(home, router.cfg, fake, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.header(stop); !strings.Contains(got, "Sessão: Primeiro pedido com quebra (") {
		t.Fatalf("título de sessão não persistido: %q", got)
	}
	reloaded.HandleEvent(context.Background(), model.Event{Type: model.EventSessionEnd, SessionID: "S"})
	if got := reloaded.header(stop); got != "**Devin · PC-TESTE · demo**\nSessão: S\n" {
		t.Fatalf("título não removido no fim da sessão: %q", got)
	}
}

func TestAwayOffStopSendsNotificationWithoutBlocking(t *testing.T) {
	router, fake, _ := newTestRouter(t, time.Second)
	router.SetAway(false)
	value := event(model.EventStop, "present-session")
	value.Message = "Full final response for phone notification"
	got := router.HandleEvent(context.Background(), value)
	if got.Action != model.ActionNone {
		t.Fatalf("modo presente bloqueou o agente: %+v", got)
	}
	messages := waitForSent(t, fake, 1)
	if !strings.Contains(messages[0].Text, value.Message) {
		t.Fatalf("notificação ausente ou truncada: %+v", messages[0])
	}
}

func TestAwayOffQuestionNotificationListsOptions(t *testing.T) {
	router, fake, _ := newTestRouter(t, time.Second)
	router.SetAway(false)
	value := event(model.EventQuestion, "present-question")
	value.ToolName = "ask_user_question"
	value.Questions = []model.Question{
		{
			Text:   "Qual resposta?",
			Header: "Selecao",
			Options: []model.Option{
				{Label: "Sim", Description: "ok"},
				{Label: "Nao"},
			},
		},
		{
			Text:        "Quais frutas?",
			MultiSelect: true,
			Options: []model.Option{
				{Label: "Maca"},
				{Label: "Banana"},
			},
		},
	}
	got := router.HandleEvent(context.Background(), value)
	if got.Action != model.ActionNone {
		t.Fatalf("modo presente bloqueou o agente: %+v", got)
	}
	messages := waitForSent(t, fake, 1)
	text := messages[0].Text
	for _, want := range []string{"Selecao: ", "- Sim — ok", "- Nao", "- Maca", "- Banana", "(múltipla escolha)", "/away"} {
		if !strings.Contains(text, want) {
			t.Fatalf("notificação sem %q: %q", want, text)
		}
	}
	if messages[0].Keyboard != nil {
		t.Fatalf("teclado inesperado: %+v", messages[0].Keyboard)
	}
}
