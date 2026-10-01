package daemon

import (
	"context"
	"errors"
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
	router, err := NewRouter(home, cfg, []channel.Channel{fake}, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	router.SetAway(true)
	return router, fake, home
}

func newMultiTestRouter(t *testing.T, wait time.Duration, channels ...channel.Channel) *Router {
	t.Helper()
	cfg := config.Default()
	cfg.StopWait = wait
	cfg.PermissionWait = wait
	cfg.QuestionWait = wait
	cfg.Telegram.ChatID = 123
	cfg.MachineName = "PC-TESTE"
	router, err := NewRouter(t.TempDir(), cfg, channels, logx.New(""))
	if err != nil {
		t.Fatal(err)
	}
	router.SetAway(true)
	return router
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
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, Text: "Agora responda com BANANA", ReplyToMessage: sent[0].ID})
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
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, Text: "CONTINUE"})
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
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, Text: "Resposta roteada"})
	sent := waitForSent(t, fake, 3)
	if len(sent[2].Keyboard) != 2 || len(sent[2].Keyboard[0]) != 1 {
		t.Fatalf("picker inválido: %+v", sent[2].Keyboard)
	}
	selected := sent[2].Keyboard[0][0].Data
	if !strings.HasPrefix(selected, "r:") {
		t.Fatalf("callback inesperado: %s", selected)
	}
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, CallbackID: "cb-route", CallbackData: selected})
	if actions := fake.ActionSnapshot(); len(actions) != 2 || actions[0] != "request-text" || actions[1] != "answer-callback" {
		t.Fatalf("RequestText deve reconhecer o callback antes do envio: %v", actions)
	}
	sent = waitForSent(t, fake, 4)
	if !sent[3].ForceReply {
		t.Fatal("esperava ForceReply após escolher sessão")
	}
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, Text: "BANANA", ReplyToMessage: sent[3].ID})
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
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, Text: "continue depois", ReplyToMessage: sent[0].ID})
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
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, CallbackID: "cb-deny", CallbackData: instructionButton.Data})
	if actions := fake.ActionSnapshot(); len(actions) < 2 || actions[0] != "request-text" || actions[1] != "answer-callback" {
		t.Fatalf("RequestText de permissão adiantou callback: %v", actions)
	}
	sent = waitForSent(t, fake, 2)
	if !sent[1].ForceReply {
		t.Fatal("esperava force reply para instrução de negação")
	}
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, Text: "Use a pasta temp", ReplyToMessage: sent[1].ID})
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
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, CallbackID: "cb-other", CallbackData: otherButton.Data})
	if actions := fake.ActionSnapshot(); len(actions) != 4 || actions[2] != "request-text" || actions[3] != "answer-callback" {
		t.Fatalf("RequestText de pergunta adiantou callback: %v", actions)
	}
	sent = waitForSent(t, fake, 4)
	if !sent[3].ForceReply {
		t.Fatal("Outro (texto) deve abrir ForceReply")
	}
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, Text: "Roxo", ReplyToMessage: sent[3].ID})
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
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, CallbackID: "cb-toggle", CallbackData: firstButton.Data})
	sent = waitForSent(t, fake, 1)
	_, edits := fake.Snapshot()
	if len(edits) == 0 || !strings.Contains(edits[0].Text, "**Frutas**\nEscolha") {
		t.Fatalf("edição da pergunta não reutilizou o texto formatado: %+v", edits)
	}
	confirm := sent[0].Keyboard[len(sent[0].Keyboard)-1][0]
	if confirm.Text != "Confirmar" {
		t.Fatalf("botão de confirmação ausente: %+v", sent[0].Keyboard)
	}
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", ChatID: 123, CallbackID: "cb-confirm", CallbackData: confirm.Data})
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

	reloaded, err := NewRouter(home, router.cfg, []channel.Channel{fake}, logx.New(""))
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

func TestMultiChannelPermissionFirstAnswerWins(t *testing.T) {
	telegram := fakechannel.New()
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, telegram, discord)
	value := event(model.EventPermission, "permission-multi")
	value.SessionTitle = "Aprovar comando"
	result := runEvent(router, value)
	telegramMessages := waitForSent(t, telegram, 1)
	discordMessages := waitForSent(t, discord, 1)
	wantSession := channel.SessionRef{ID: value.SessionID, Name: value.SessionName, Title: value.SessionTitle, Project: value.Project, Agent: string(value.Agent)}
	if telegramMessages[0].Session != wantSession || discordMessages[0].Session != wantSession {
		t.Fatalf("SessionRef Telegram=%+v Discord=%+v, esperado %+v", telegramMessages[0].Session, discordMessages[0].Session, wantSession)
	}
	if len(telegramMessages[0].Keyboard) == 0 || len(discordMessages[0].Keyboard) == 0 {
		t.Fatal("aprovação não foi enviada com botões aos dois canais")
	}
	if telegramMessages[0].Keyboard[0][0].Style != "success" || telegramMessages[0].Keyboard[0][1].Style != "danger" {
		t.Fatalf("estilos de aprovação=%+v", telegramMessages[0].Keyboard)
	}
	approve := discordMessages[0].Keyboard[0][0]
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Discord", CallbackID: "discord-approve", CallbackData: approve.Data})
	if got := <-result; got.Action != model.ActionApprove {
		t.Fatalf("resolução=%+v", got)
	}
	_, telegramEdits := telegram.Snapshot()
	foundStatus := false
	for _, edit := range telegramEdits {
		if strings.Contains(edit.Text, "Aprovado pelo usuário via Discord") {
			foundStatus = true
		}
	}
	if !foundStatus {
		t.Fatalf("mensagem do Telegram sem status de origem Discord: %+v", telegramEdits)
	}
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Telegram", CallbackID: "telegram-late", CallbackData: telegramMessages[0].Keyboard[0][0].Data})
	_, callbackTexts := telegram.CallbackSnapshot()
	if len(callbackTexts) == 0 || callbackTexts[len(callbackTexts)-1] != "Esta decisão expirou." {
		t.Fatalf("clique tardio não expirou: %v", callbackTexts)
	}
	select {
	case extra := <-result:
		t.Fatalf("segunda resolução inesperada: %+v", extra)
	default:
	}
}

func TestMultiChannelQuestionRequestsTextOnlyFromOrigin(t *testing.T) {
	telegram := fakechannel.New()
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, telegram, discord)
	value := event(model.EventQuestion, "question-multi")
	value.Questions = []model.Question{{Text: "Qual cor?", Options: []model.Option{{Label: "Azul"}, {Label: "Verde"}}}}
	result := runEvent(router, value)
	waitForSent(t, telegram, 1)
	discordMessages := waitForSent(t, discord, 1)
	other := discordMessages[0].Keyboard[len(discordMessages[0].Keyboard)-1][0]
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Discord", CallbackID: "discord-other", CallbackData: other.Data})
	if actions := discord.ActionSnapshot(); len(actions) != 1 || actions[0] != "request-text" {
		t.Fatalf("callback foi respondido antes do modal: %v", actions)
	}
	discordRequests, _ := discord.Snapshot()
	var token string
	for _, request := range discordRequests {
		if request.TextRequest {
			token = request.Token
			if request.Text != "Outro (texto) — responda à pergunta: Qual cor?" {
				t.Fatalf("modal com prompt inesperado: %+v", request)
			}
		}
	}
	if token != "t:"+strings.Split(other.Data, ":")[1] {
		t.Fatalf("token de texto=%q, callback=%q", token, other.Data)
	}
	telegramMessages, _ := telegram.Snapshot()
	for _, message := range telegramMessages {
		if message.TextRequest {
			t.Fatalf("RequestText também enviado ao Telegram: %+v", message)
		}
	}
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Discord", TextToken: token, Text: "Roxo"})
	got := <-result
	if got.Action != model.ActionBlock || !strings.Contains(got.Reason, "Roxo") {
		t.Fatalf("resposta via token=%+v", got)
	}
}

func TestEditPendingUsesEachChannelMessageLimit(t *testing.T) {
	telegram := fakechannel.New()
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, telegram, discord)
	value := event(model.EventStop, "channel-limits")
	prefix := router.header(value) + "\n"
	value.Message = strings.Repeat("x", 3950-len([]rune(prefix)))
	result := runEvent(router, value)
	telegramMessages := waitForSent(t, telegram, 1)
	discordMessages := waitForSent(t, discord, 2)
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Discord", ReplyToMessage: discordMessages[0].ID, Text: "continue"})
	if got := <-result; got.Action != model.ActionBlock {
		t.Fatalf("resolução=%+v", got)
	}
	_, telegramEdits := telegram.Snapshot()
	if len(telegramEdits) != 1 || !strings.Contains(telegramEdits[0].Text, "Resposta recebida via Discord") {
		t.Fatalf("Telegram deveria editar a mensagem dentro do próprio limite: %+v", telegramEdits)
	}
	discordMessages, _ = discord.Snapshot()
	if len(discordMessages) != 3 || discordMessages[2].ReplyTo != discordMessages[1].ID || !strings.Contains(discordMessages[2].Text, "Resposta recebida via Discord") {
		t.Fatalf("Discord deveria enviar status vinculado ao segmento final: %+v", discordMessages)
	}
	if len(telegramMessages) != 1 {
		t.Fatalf("quantidade de mensagens Telegram=%d", len(telegramMessages))
	}
}

func TestSessionThreadRoutesPendingAndLateRepliesToOrigin(t *testing.T) {
	telegram := fakechannel.New()
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, telegram, discord)
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Discord", SessionID: "late-session", Text: "responda depois"})
	discordMessages := waitForSent(t, discord, 1)
	if discordMessages[0].Session.ID != "late-session" || !strings.Contains(discordMessages[0].Text, "Enfileirado") {
		t.Fatalf("resposta tardia não foi enviada à thread de origem: %+v", discordMessages[0])
	}
	if messages, _ := telegram.Snapshot(); len(messages) != 0 {
		t.Fatalf("resposta tardia também enviada ao Telegram: %+v", messages)
	}

	result := runEvent(router, event(model.EventStop, "waiting-session"))
	waitForSent(t, telegram, 1)
	waitForSent(t, discord, 2)
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Discord", SessionID: "waiting-session", Text: "continue"})
	got := <-result
	if got.Action != model.ActionBlock || !strings.Contains(got.Reason, mobilePrefix+"continue") {
		t.Fatalf("resposta da thread não resolveu Stop como esperado: %+v", got)
	}
}

func TestMultiChannelSendFailureIsFailOpenOnlyWhenAllFail(t *testing.T) {
	telegram := fakechannel.New()
	telegram.SendError = errors.New("Telegram indisponível")
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, telegram, discord)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan model.Resolution, 1)
	go func() { result <- router.HandleEvent(ctx, event(model.EventStop, "one-channel-fails")) }()
	waitForSent(t, discord, 1)
	cancel()
	if got := <-result; got.Action != model.ActionNone {
		t.Fatalf("cancelamento após entrega parcial=%+v", got)
	}

	telegramAllFail := fakechannel.New()
	discordAllFail := fakechannel.NewNamed("Discord", 2000)
	telegramAllFail.SendError = errors.New("Telegram indisponível")
	discordAllFail.SendError = errors.New("Discord indisponível")
	routerAllFail := newMultiTestRouter(t, time.Second, telegramAllFail, discordAllFail)
	if got := routerAllFail.HandleEvent(context.Background(), event(model.EventStop, "all-channels-fail")); got.Action != model.ActionNone {
		t.Fatalf("falha de todos os canais não liberou o hook: %+v", got)
	}
}

func TestUnknownChannelUpdateIsIgnored(t *testing.T) {
	telegram := fakechannel.New()
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, telegram, discord)
	router.HandleUpdate(context.Background(), channel.Update{Channel: "Unknown", SessionID: "ignored-session", Text: "não enfileirar"})
	if sent, _ := telegram.Snapshot(); len(sent) != 0 {
		t.Fatalf("update desconhecido enviou ao Telegram: %+v", sent)
	}
	if sent, _ := discord.Snapshot(); len(sent) != 0 {
		t.Fatalf("update desconhecido enviou ao Discord: %+v", sent)
	}
	router.mu.Lock()
	queued := len(router.state.Queues["ignored-session"])
	router.mu.Unlock()
	if queued != 0 {
		t.Fatalf("update desconhecido foi enfileirado: %d", queued)
	}
}

func progressEvent(session, turn, tool, summary string, failed bool) model.Event {
	value := event(model.EventProgress, session)
	value.TurnID = turn
	value.ToolName = tool
	value.ToolSummary = summary
	value.ToolFailed = failed
	return value
}

func waitForProgressIdle(t *testing.T, router *Router, session string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		router.mu.Lock()
		panel := router.progressPanels[session]
		idle := panel != nil && !panel.inFlight && panel.timer == nil
		router.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("painel de andamento não terminou o flush")
}

func waitForDiscordEdits(t *testing.T, discord *fakechannel.Fake, count int) []fakechannel.EditRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, edits := discord.Snapshot()
		if len(edits) >= count {
			return edits
		}
		time.Sleep(2 * time.Millisecond)
	}
	_, edits := discord.Snapshot()
	t.Fatalf("esperava %d edições do painel; recebeu %d", count, len(edits))
	return nil
}

func TestProgressPanelDiscordOnlyAndCoalescesBursts(t *testing.T) {
	telegram := fakechannel.New()
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, telegram, discord)
	router.progressInterval = 40 * time.Millisecond
	first := progressEvent("progress-session", "turn-1", "exec", "secret-command", false)
	if got := router.HandleEvent(context.Background(), first); got.Action != model.ActionNone {
		t.Fatalf("progress bloqueou hook: %+v", got)
	}
	messages := waitForSent(t, discord, 1)
	waitForProgressIdle(t, router, first.SessionID)
	wantSession := channel.SessionRef{ID: first.SessionID, Name: first.SessionName, Title: first.SessionTitle, Project: first.Project, Agent: string(first.Agent)}
	if messages[0].Session != wantSession || !strings.Contains(messages[0].Text, "**Andamento do turno**") {
		t.Fatalf("painel inicial=%+v", messages[0])
	}
	for index := 2; index <= 5; index++ {
		if got := router.HandleEvent(context.Background(), progressEvent(first.SessionID, "turn-1", "exec", "secret-command", false)); got.Action != model.ActionNone {
			t.Fatalf("progress %d bloqueou hook: %+v", index, got)
		}
	}
	edits := waitForDiscordEdits(t, discord, 1)
	if len(edits) != 1 || !strings.Contains(edits[0].Text, "5 ações") || strings.Contains(edits[0].Text, "secret-command") || strings.Contains(edits[0].Text, "`exec`") {
		t.Fatalf("flush resumido não coalesceu ou expôs ferramenta: %+v", edits)
	}
	if sent, _ := telegram.Snapshot(); len(sent) != 0 {
		t.Fatalf("Telegram recebeu painel de andamento: %+v", sent)
	}
	router.mu.Lock()
	pendingMessages := len(router.byMessage) + len(router.lateMessage)
	router.mu.Unlock()
	if pendingMessages != 0 {
		t.Fatalf("painel entrou no bookkeeping de respostas: %d", pendingMessages)
	}
}

func TestProgressPanelCompleteShowsOnlyRecentActionsAndFailure(t *testing.T) {
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, discord)
	router.progressInterval = 30 * time.Millisecond
	router.cfg.ProgressDetail = "completo"
	session := "complete-progress"
	if got := router.HandleEvent(context.Background(), progressEvent(session, "turn", "exec", "old command", false)); got.Action != model.ActionNone {
		t.Fatalf("progress bloqueou hook: %+v", got)
	}
	waitForSent(t, discord, 1)
	waitForProgressIdle(t, router, session)
	actions := []progressAction{
		{category: progressEdits, toolName: "edit", summary: "router.go"},
		{category: progressReads, toolName: "read", summary: "config.go"},
		{category: progressSearches, toolName: "grep", summary: "ProgressCapable"},
		{category: progressOther, toolName: "todo_write"},
		{category: progressCommands, toolName: "exec", summary: "go vet ./...", failed: true},
		{category: progressSearches, toolName: "web_search", summary: "intent API"},
	}
	for _, action := range actions {
		value := progressEvent(session, "turn", action.toolName, action.summary, action.failed)
		if got := router.HandleEvent(context.Background(), value); got.Action != model.ActionNone {
			t.Fatalf("progress bloqueou hook: %+v", got)
		}
	}
	edits := waitForDiscordEdits(t, discord, 1)
	text := edits[0].Text
	for _, want := range []string{"Últimas ações:", "editou `router.go`", "leu `config.go`", "buscou `ProgressCapable`", "`todo_write`", "falhou: `go vet ./...`", "buscou `intent API`"} {
		if !strings.Contains(text, want) {
			t.Fatalf("painel completo sem %q: %q", want, text)
		}
	}
	if strings.Contains(text, "old command") || strings.Contains(text, "`exec`") {
		t.Fatalf("painel não manteve apenas as últimas seis ações: %q", text)
	}
}

func TestProgressStopFinalizesTurnAndSessionEndCancelsTimer(t *testing.T) {
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, discord)
	router.cfg.NotifyWhenPresent = false
	router.SetAway(false)
	router.progressInterval = time.Hour
	session := "finish-progress"
	if got := router.HandleEvent(context.Background(), progressEvent(session, "turn-1", "read", "main.go", false)); got.Action != model.ActionNone {
		t.Fatalf("progress bloqueou hook: %+v", got)
	}
	waitForSent(t, discord, 1)
	waitForProgressIdle(t, router, session)
	if got := router.HandleEvent(context.Background(), event(model.EventStop, session)); got.Action != model.ActionNone {
		t.Fatalf("Stop presente bloqueou hook: %+v", got)
	}
	edits := waitForDiscordEdits(t, discord, 1)
	if !strings.Contains(edits[0].Text, "concluído em") {
		t.Fatalf("painel final não marcou a duração: %q", edits[0].Text)
	}
	if got := router.HandleEvent(context.Background(), progressEvent(session, "turn-2", "exec", "go test", false)); got.Action != model.ActionNone {
		t.Fatalf("novo turno bloqueou hook: %+v", got)
	}
	waitForSent(t, discord, 2)
	waitForProgressIdle(t, router, session)
	if got := router.HandleEvent(context.Background(), progressEvent(session, "turn-2", "read", "file.go", false)); got.Action != model.ActionNone {
		t.Fatalf("progress bloqueou hook: %+v", got)
	}
	router.HandleEvent(context.Background(), model.Event{Type: model.EventSessionEnd, SessionID: session})
	time.Sleep(40 * time.Millisecond)
	_, edits = discord.Snapshot()
	if len(edits) != 1 {
		t.Fatalf("SessionEnd não cancelou o timer do painel: %+v", edits)
	}
	router.mu.Lock()
	_, exists := router.progressPanels[session]
	router.mu.Unlock()
	if exists {
		t.Fatal("SessionEnd não removeu o painel")
	}
}

func TestPromptDropsUnfinishedProgressPanel(t *testing.T) {
	discord := fakechannel.NewNamed("Discord", 2000)
	router := newMultiTestRouter(t, time.Second, discord)
	router.progressInterval = time.Hour
	session := "prompt-progress"
	if got := router.HandleEvent(context.Background(), progressEvent(session, "turn", "exec", "go test", false)); got.Action != model.ActionNone {
		t.Fatalf("progress bloqueou hook: %+v", got)
	}
	waitForSent(t, discord, 1)
	waitForProgressIdle(t, router, session)
	prompt := event(model.EventPrompt, session)
	prompt.Prompt = "novo turno"
	if got := router.HandleEvent(context.Background(), prompt); got.Action != model.ActionContext {
		t.Fatalf("prompt=%+v", got)
	}
	if got := router.HandleEvent(context.Background(), progressEvent(session, "turn", "read", "file.go", false)); got.Action != model.ActionNone {
		t.Fatalf("novo painel bloqueou hook: %+v", got)
	}
	waitForSent(t, discord, 2)
}

func TestProgressReturnsWithoutWaitingForChannelSend(t *testing.T) {
	discord := fakechannel.NewNamed("Discord", 2000)
	release := make(chan struct{})
	discord.BlockSend = release
	discord.SendStarted = make(chan struct{}, 1)
	router := newMultiTestRouter(t, time.Second, discord)
	start := time.Now()
	if got := router.HandleEvent(context.Background(), progressEvent("blocked-progress", "turn", "exec", "slow command", false)); got.Action != model.ActionNone {
		t.Fatalf("progress bloqueou hook: %+v", got)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("hook esperou o envio por %s", elapsed)
	}
	select {
	case <-discord.SendStarted:
	case <-time.After(time.Second):
		t.Fatal("envio bloqueado não começou")
	}
	close(release)
	waitForSent(t, discord, 1)
	waitForProgressIdle(t, router, "blocked-progress")
}

func TestProgressWithoutCapableChannelSendsNothing(t *testing.T) {
	telegram := fakechannel.New()
	router := newMultiTestRouter(t, time.Second, telegram)
	if got := router.HandleEvent(context.Background(), progressEvent("no-progress", "turn", "exec", "go test", false)); got.Action != model.ActionNone {
		t.Fatalf("progress bloqueou hook: %+v", got)
	}
	if sent, _ := telegram.Snapshot(); len(sent) != 0 {
		t.Fatalf("Telegram recebeu painel: %+v", sent)
	}
}
