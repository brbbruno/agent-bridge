package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/logx"
	"github.com/brbbruno/agent-bridge/internal/model"
)

const mobilePrefix = "Mensagem do usuário (via celular): "

type questionProgress struct {
	selected map[int]bool
	answer   string
	done     bool
	message  int64
}

type pending struct {
	id                  string
	event               model.Event
	result              chan model.Resolution
	messages            []int64
	messageText         map[int64]string
	questionMessages    map[int64]int
	forceReplyMessages  map[int64]bool
	mainMessage         int64
	questions           []questionProgress
	awaitingText        string
	awaitingQuestion    int
	awaitingTextMessage int64
	completed           bool
}

type Router struct {
	mu            sync.Mutex
	home          string
	cfg           config.Config
	state         persistedState
	channel       channel.Channel
	logger        *logx.Logger
	started       time.Time
	pending       map[string]*pending
	waiting       map[string]string
	byMessage     map[int64]string
	lateMessage   map[int64]lateMessageEntry
	sessionLabels map[string]string
	endedSessions map[string]time.Time
	idCounter     uint64
}

func NewRouter(home string, cfg config.Config, telegram channel.Channel, logger *logx.Logger) (*Router, error) {
	state, err := loadState(home)
	if err != nil {
		return nil, err
	}
	return &Router{
		home:          home,
		cfg:           cfg,
		state:         state,
		channel:       telegram,
		logger:        logger,
		started:       time.Now(),
		pending:       map[string]*pending{},
		waiting:       map[string]string{},
		byMessage:     map[int64]string{},
		lateMessage:   map[int64]lateMessageEntry{},
		sessionLabels: map[string]string{},
		endedSessions: map[string]time.Time{},
	}, nil
}

func (r *Router) HandleEvent(ctx context.Context, event model.Event) model.Resolution {
	if event.Type == model.EventSessionEnd {
		r.endSession(event.SessionID)
		return model.Resolution{Action: model.ActionNone}
	}
	r.mu.Lock()
	delete(r.endedSessions, event.SessionID)
	r.mu.Unlock()
	if event.Type == model.EventPrompt {
		if r.Away() {
			return model.Resolution{Action: model.ActionContext, Context: "O usuário está em modo ausente e acompanha pelo celular. Ao terminar ou precisar de uma decisão, encerre o turno com uma mensagem clara e autocontida."}
		}
		return model.Resolution{Action: model.ActionNone}
	}

	r.mu.Lock()
	away := r.state.Away
	if event.Type == model.EventStop && away {
		if replies := r.state.Queues[event.SessionID]; len(replies) > 0 {
			delete(r.state.Queues, event.SessionID)
			r.pruneSessionLabelsLocked()
			if err := saveState(r.home, r.state); err != nil {
				r.logger.Errorf("salvar fila de respostas: %v", err)
			}
			r.mu.Unlock()
			return model.Resolution{Action: model.ActionBlock, Reason: mobilePrefix + strings.Join(replies, "\n")}
		}
	}
	r.mu.Unlock()

	if !away {
		if r.cfg.NotifyWhenPresent {
			r.notifyPresent(event)
		}
		return model.Resolution{Action: model.ActionNone}
	}
	if r.channel == nil {
		r.logger.Errorf("canal Telegram não configurado; hook liberado para a sessão %s", event.SessionID)
		return model.Resolution{Action: model.ActionNone}
	}
	if event.Type != model.EventStop && event.Type != model.EventPermission && event.Type != model.EventQuestion {
		return model.Resolution{Action: model.ActionNone}
	}
	return r.waitForUser(ctx, event)
}

func (r *Router) waitForUser(ctx context.Context, event model.Event) model.Resolution {
	p := &pending{
		id:                 r.newID(),
		event:              event,
		result:             make(chan model.Resolution, 1),
		messageText:        map[int64]string{},
		questionMessages:   map[int64]int{},
		forceReplyMessages: map[int64]bool{},
		awaitingQuestion:   -1,
	}
	if event.Type == model.EventQuestion {
		p.questions = make([]questionProgress, len(event.Questions))
		for i := range p.questions {
			p.questions[i].selected = map[int]bool{}
		}
	}
	r.mu.Lock()
	r.pending[p.id] = p
	r.waiting[event.SessionID] = p.id
	r.sessionLabels[event.SessionID] = event.SessionID
	r.sessionLabels[event.SessionName] = event.SessionID
	r.mu.Unlock()

	if event.Type == model.EventQuestion {
		for index := range event.Questions {
			text, keyboard := r.questionPrompt(event, p.id, index, p.questions[index].selected)
			message, err := r.channel.Send(ctx, text, keyboard, false)
			if err != nil {
				r.logger.Errorf("enviar pergunta ao Telegram: %v", err)
				r.finish(p, false)
				return model.Resolution{Action: model.ActionNone}
			}
			r.addQuestionMessage(p, index, message, text)
		}
	} else {
		text := r.header(event) + "\n"
		switch event.Type {
		case model.EventStop:
			text += event.Message
			if strings.TrimSpace(event.Message) == "" {
				text += "(o agente encerrou o turno sem mensagem final)"
			}
		case model.EventPermission:
			text += "Precisa de aprovação: " + event.ToolName
			if event.ToolSummary != "" {
				text += "\n" + event.ToolSummary
			}
		}
		keyboard := channel.Keyboard(nil)
		if event.Type == model.EventPermission {
			keyboard = channel.Keyboard{{
				{Text: "Aprovar", Data: "p:" + p.id + ":a"},
				{Text: "Negar", Data: "p:" + p.id + ":d"},
				{Text: "Negar com instrução", Data: "p:" + p.id + ":i"},
			}}
		}
		message, err := r.channel.Send(ctx, text, keyboard, false)
		if err != nil {
			r.logger.Errorf("enviar solicitação ao Telegram: %v", err)
			r.finish(p, false)
			return model.Resolution{Action: model.ActionNone}
		}
		r.addMainMessage(p, message, text)
	}

	wait := r.cfg.WaitFor(string(event.Type))
	if wait <= 0 {
		wait = 30 * time.Minute
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case result := <-p.result:
		r.finish(p, true)
		r.editPending(p, resolutionStatus(event.Type, result))
		return result
	case <-ctx.Done():
		r.finish(p, true)
		r.editPending(p, "Cancelado: o turno do agente foi interrompido.")
		return model.Resolution{Action: model.ActionNone}
	case <-timer.C:
		r.finish(p, true)
		switch event.Type {
		case model.EventQuestion:
			r.editPending(p, "Tempo esgotado; o agente continuará sem suas respostas.")
			return model.Resolution{Action: model.ActionBlock, Reason: "O usuário está ausente e não respondeu; faça as perguntas em texto no fim do turno."}
		case model.EventPermission:
			r.editPending(p, "Tempo esgotado; a decisão voltará ao computador.")
		default:
			r.editPending(p, "Tempo esgotado; o agente encerrou o turno.")
		}
		return model.Resolution{Action: model.ActionNone}
	}
}

func (r *Router) notifyPresent(event model.Event) {
	if r.channel == nil {
		return
	}
	text := r.header(event)
	switch event.Type {
	case model.EventStop:
		text += "\n" + event.Message
	case model.EventPermission:
		text += "\nPrecisa de aprovação: " + event.ToolName
		if event.ToolSummary != "" {
			text += "\n" + event.ToolSummary
		}
	case model.EventQuestion:
		for index, question := range event.Questions {
			header := ""
			if question.Header != "" {
				header = question.Header + ": "
			}
			text += fmt.Sprintf("\n%d. %s%s", index+1, header, question.Text)
			for _, option := range question.Options {
				text += "\n   - " + option.Label
				if option.Description != "" {
					text += " — " + option.Description
				}
			}
			if question.MultiSelect {
				text += "\n   (múltipla escolha)"
			}
		}
		text += "\nResponda no computador ou ative o modo ausente (/away) para responder pelo celular com botões."
	default:
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		message, err := r.channel.Send(ctx, text, nil, false)
		if err != nil {
			r.logger.Errorf("enviar notificação: %v", err)
			return
		}
		r.mu.Lock()
		if _, ended := r.endedSessions[event.SessionID]; ended {
			r.mu.Unlock()
			return
		}
		r.addLateMessagesLocked(message.IDs, event.SessionID, time.Now())
		r.sessionLabels[event.SessionID] = event.SessionID
		r.sessionLabels[event.SessionName] = event.SessionID
		r.pruneSessionLabelsLocked()
		r.mu.Unlock()
	}()
}

func (r *Router) HandleUpdate(ctx context.Context, update channel.Update) {
	if update.ChatID != r.cfg.Telegram.ChatID {
		return
	}
	if update.CallbackID != "" {
		r.handleCallback(ctx, update)
		return
	}
	if strings.TrimSpace(update.Text) != "" {
		r.handleText(ctx, update)
	}
}

func (r *Router) handleCallback(ctx context.Context, update channel.Update) {
	parts := strings.Split(update.CallbackData, ":")
	if len(parts) == 0 {
		_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Ação inválida.")
		return
	}
	if parts[0] == "r" && len(parts) == 2 {
		r.handleRouteCallback(ctx, update, parts[1])
		return
	}
	if parts[0] == "p" && len(parts) == 3 {
		r.handlePermissionCallback(ctx, update, parts[1], parts[2])
		return
	}
	if parts[0] == "q" && len(parts) == 4 {
		r.handleQuestionCallback(ctx, update, parts[1], parts[2], parts[3])
		return
	}
	_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Ação inválida.")
}

func (r *Router) handlePermissionCallback(ctx context.Context, update channel.Update, id, action string) {
	r.mu.Lock()
	p := r.pending[id]
	if p == nil || p.event.Type != model.EventPermission {
		r.mu.Unlock()
		_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Esta decisão expirou.")
		return
	}
	r.mu.Unlock()
	_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Resposta recebida.")
	switch action {
	case "a":
		r.deliver(p, model.Resolution{Action: model.ActionApprove})
	case "d":
		r.deliver(p, model.Resolution{Action: model.ActionDeny, Reason: "O usuário negou a aprovação pelo Telegram."})
	case "i":
		r.mu.Lock()
		p.awaitingText = "permission"
		p.awaitingQuestion = -1
		r.mu.Unlock()
		forceReplyText := "Digite a instrução para negar a ação:"
		message, err := r.channel.Send(ctx, forceReplyText, nil, true)
		if err != nil {
			r.logger.Errorf("pedir instrução de negação: %v", err)
			return
		}
		r.addForceReply(p, message, forceReplyText)
	default:
		_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Ação inválida.")
	}
}

func (r *Router) handleQuestionCallback(ctx context.Context, update channel.Update, id, questionText, action string) {
	questionIndex, err := strconv.Atoi(questionText)
	if err != nil {
		_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Pergunta inválida.")
		return
	}
	r.mu.Lock()
	p := r.pending[id]
	if p == nil || p.event.Type != model.EventQuestion || questionIndex < 0 || questionIndex >= len(p.questions) {
		r.mu.Unlock()
		_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Esta pergunta expirou.")
		return
	}
	question := p.event.Questions[questionIndex]
	progress := &p.questions[questionIndex]
	if action == "o" {
		p.awaitingText = "question"
		p.awaitingQuestion = questionIndex
		r.mu.Unlock()
		_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Responda em seguida.")
		forceReplyText := "Outro (texto) — responda à pergunta: " + question.Text
		message, err := r.channel.Send(ctx, forceReplyText, nil, true)
		if err != nil {
			r.logger.Errorf("pedir resposta livre: %v", err)
			return
		}
		r.addForceReply(p, message, forceReplyText)
		return
	}
	if action == "c" && question.MultiSelect {
		labels := selectedLabels(question, progress.selected)
		progress.answer = strings.Join(labels, ", ")
		if progress.answer == "" {
			progress.answer = "(nenhuma opção selecionada)"
		}
		progress.done = true
	} else {
		optionIndex, parseErr := strconv.Atoi(action)
		if parseErr != nil || optionIndex < 0 || optionIndex >= len(question.Options) {
			r.mu.Unlock()
			_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Opção inválida.")
			return
		}
		if question.MultiSelect {
			progress.selected[optionIndex] = !progress.selected[optionIndex]
		} else {
			progress.answer = question.Options[optionIndex].Label
			progress.done = true
		}
	}
	allDone := allQuestionsDone(p.questions)
	updatedKeyboard := questionKeyboard(id, questionIndex, question, progress.selected)
	messageID := progress.message
	event := p.event
	questions := append([]model.Question(nil), p.event.Questions...)
	answers := append([]questionProgress(nil), p.questions...)
	r.mu.Unlock()
	_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Resposta registrada.")
	if !allDone && messageID != 0 {
		if err := r.channel.Edit(ctx, messageID, r.header(event)+"\n"+question.Header+"\n"+question.Text, updatedKeyboard); err != nil {
			r.logger.Errorf("atualizar opções de pergunta: %v", err)
		}
	}
	if allDone {
		r.deliver(p, model.Resolution{Action: model.ActionBlock, Reason: questionReason(questions, answers)})
	}
}

func (r *Router) handleRouteCallback(ctx context.Context, update channel.Update, id string) {
	r.mu.Lock()
	p := r.pending[id]
	if p == nil {
		r.mu.Unlock()
		_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Esta sessão não está mais aguardando.")
		return
	}
	p.awaitingText = "route"
	r.mu.Unlock()
	_ = r.channel.AnswerCallback(ctx, update.CallbackID, "Responda à sessão selecionada.")
	forceReplyText := "Digite a mensagem para a sessão selecionada:"
	message, err := r.channel.Send(ctx, forceReplyText, nil, true)
	if err != nil {
		r.logger.Errorf("pedir mensagem para sessão: %v", err)
		return
	}
	r.addForceReply(p, message, forceReplyText)
}

func (r *Router) handleText(ctx context.Context, update channel.Update) {
	text := strings.TrimSpace(update.Text)
	if strings.HasPrefix(text, "/") {
		if r.handleCommand(ctx, text) {
			return
		}
	}
	if update.ReplyToMessage != 0 {
		r.mu.Lock()
		id := r.byMessage[update.ReplyToMessage]
		p := r.pending[id]
		late := r.lateMessage[update.ReplyToMessage]
		session := late.sessionID
		r.mu.Unlock()
		if p != nil {
			r.answerPendingText(ctx, p, update, text)
			return
		}
		if session != "" {
			r.queueLate(session, text)
			r.sendText(ctx, "Enfileirado; será entregue na próxima parada desta sessão.")
			return
		}
	}

	r.mu.Lock()
	waiting := make([]*pending, 0, len(r.waiting))
	for _, id := range r.waiting {
		if p := r.pending[id]; p != nil {
			waiting = append(waiting, p)
		}
	}
	r.mu.Unlock()
	if len(waiting) == 1 {
		r.routeTextToPending(ctx, waiting[0], text)
		return
	}
	if len(waiting) > 1 {
		keyboard := channel.Keyboard{}
		for _, p := range waiting {
			keyboard = append(keyboard, []channel.Button{{Text: string(p.event.Agent) + " | " + p.event.Project + " | " + p.event.SessionName, Data: "r:" + p.id}})
		}
		r.send(ctx, "Escolha a sessão que receberá sua mensagem:", keyboard, false)
		return
	}
	r.sendText(ctx, "Não há sessão aguardando. Responda à mensagem do agente ou use /s <sessão> <texto> para enfileirar.")
}

func (r *Router) handleCommand(ctx context.Context, text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return true
	}
	switch fields[0] {
	case "/away":
		r.setAway(true)
		r.sendText(ctx, "Modo ausente ativado.")
	case "/back":
		r.setAway(false)
		r.sendText(ctx, "Modo ausente desativado.")
	case "/list":
		r.sendText(ctx, r.waitingText())
	case "/status":
		away, count := r.Status()
		mode := "no computador"
		if away {
			mode = "ausente"
		}
		r.sendText(ctx, fmt.Sprintf("Modo: %s\nDaemon ativo há: %s\nSessões aguardando: %d", mode, time.Since(r.started).Round(time.Second), count))
	case "/help":
		r.sendText(ctx, "Comandos: /list, /status, /away, /back, /s <sessão> <texto>. Responda às mensagens do agente para direcionar uma resposta.")
	case "/s":
		if len(fields) < 3 {
			r.sendText(ctx, "Uso: /s <sessão> <texto>")
			return true
		}
		label := fields[1]
		message := strings.TrimSpace(strings.TrimPrefix(text, fields[0]+" "+label))
		r.mu.Lock()
		session := r.sessionLabels[label]
		p := r.pending[r.waiting[session]]
		r.mu.Unlock()
		if session == "" {
			r.sendText(ctx, "Sessão não encontrada. Use /list para ver as sessões.")
		} else if p != nil {
			r.routeTextToPending(ctx, p, message)
		} else {
			r.queueLate(session, message)
			r.sendText(ctx, "Enfileirado; será entregue na próxima parada desta sessão.")
		}
	default:
		return false
	}
	return true
}

func (r *Router) answerPendingText(ctx context.Context, p *pending, update channel.Update, text string) {
	r.mu.Lock()
	mode := p.awaitingText
	questionIndex := p.awaitingQuestion
	validReply := p.awaitingTextMessage == 0 || update.ReplyToMessage == p.awaitingTextMessage
	if mode == "" || !validReply {
		r.mu.Unlock()
		r.routeTextToPending(ctx, p, text)
		return
	}
	p.awaitingText = ""
	p.awaitingTextMessage = 0
	switch mode {
	case "permission":
		r.mu.Unlock()
		r.deliver(p, model.Resolution{Action: model.ActionDeny, Reason: "Mensagem do usuário (via celular): " + text})
	case "question":
		if questionIndex >= 0 && questionIndex < len(p.questions) {
			answerQuestionWithText(p.event.Questions[questionIndex], &p.questions[questionIndex], text)
			p.questions[questionIndex].done = true
		}
		allDone := allQuestionsDone(p.questions)
		questions := append([]model.Question(nil), p.event.Questions...)
		answers := append([]questionProgress(nil), p.questions...)
		r.mu.Unlock()
		if allDone {
			r.deliver(p, model.Resolution{Action: model.ActionBlock, Reason: questionReason(questions, answers)})
		} else {
			r.sendText(ctx, "Resposta registrada. Ainda aguardando outras perguntas.")
		}
	case "route":
		r.mu.Unlock()
		r.routeTextToPending(ctx, p, text)
	default:
		r.mu.Unlock()
		r.routeTextToPending(ctx, p, text)
	}
}

func (r *Router) routeTextToPending(ctx context.Context, p *pending, text string) {
	switch p.event.Type {
	case model.EventStop:
		r.deliver(p, model.Resolution{Action: model.ActionBlock, Reason: mobilePrefix + text})
	case model.EventPermission:
		r.deliver(p, model.Resolution{Action: model.ActionDeny, Reason: "Mensagem do usuário (via celular): " + text})
	case model.EventQuestion:
		r.mu.Lock()
		index := firstUnanswered(p.questions)
		if index >= 0 {
			answerQuestionWithText(p.event.Questions[index], &p.questions[index], text)
			p.questions[index].done = true
		}
		allDone := allQuestionsDone(p.questions)
		questions := append([]model.Question(nil), p.event.Questions...)
		answers := append([]questionProgress(nil), p.questions...)
		r.mu.Unlock()
		if allDone {
			r.deliver(p, model.Resolution{Action: model.ActionBlock, Reason: questionReason(questions, answers)})
		} else {
			r.send(ctx, "Resposta registrada. Ainda aguardando outras perguntas.", nil, false)
		}
	}
}

func (r *Router) deliver(p *pending, result model.Resolution) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.pending[p.id]
	if current == nil || current.completed {
		return
	}
	current.completed = true
	select {
	case current.result <- result:
	default:
	}
}

func (r *Router) finish(p *pending, keepLate bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := r.pending[p.id] != nil
	delete(r.pending, p.id)
	if r.waiting[p.event.SessionID] == p.id {
		delete(r.waiting, p.event.SessionID)
	}
	for _, messageID := range p.messages {
		delete(r.byMessage, messageID)
	}
	if keepLate && active {
		r.addLateMessagesLocked(p.messages, p.event.SessionID, time.Now())
	}
	r.pruneSessionLabelsLocked()
}

func resolutionStatus(eventType model.EventType, resolution model.Resolution) string {
	if eventType == model.EventPermission {
		if resolution.Action == model.ActionApprove {
			return "Aprovado pelo usuário; decisão enviada ao agente."
		}
		if resolution.Action == model.ActionDeny {
			return "Negado pelo usuário; decisão enviada ao agente."
		}
	}
	return "Resposta recebida; será entregue ao agente."
}

func (r *Router) addMainMessage(p *pending, message channel.SentMessage, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordMessageLocked(p, message, text)
	p.mainMessage = message.ID
	if p.mainMessage == 0 && len(message.IDs) > 0 {
		p.mainMessage = message.IDs[len(message.IDs)-1]
	}
}

func (r *Router) addForceReply(p *pending, message channel.SentMessage, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p.awaitingTextMessage = message.ID
	r.recordMessageLocked(p, message, text)
	if message.ID != 0 {
		p.forceReplyMessages[message.ID] = true
	}
}

func (r *Router) addQuestionMessage(p *pending, index int, message channel.SentMessage, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordMessageLocked(p, message, text)
	if index >= 0 && index < len(p.questions) {
		p.questions[index].message = message.ID
		if message.ID != 0 {
			p.questionMessages[message.ID] = index
		}
	}
}

func (r *Router) recordMessageLocked(p *pending, message channel.SentMessage, fallbackText string) {
	ids := message.IDs
	if len(ids) == 0 && message.ID != 0 {
		ids = []int64{message.ID}
	}
	for index, id := range ids {
		if id == 0 {
			continue
		}
		text := fallbackText
		if index < len(message.Chunks) {
			text = message.Chunks[index]
		}
		p.messages = append(p.messages, id)
		p.messageText[id] = text
		r.byMessage[id] = p.id
	}
}

func (r *Router) editPending(p *pending, status string) {
	r.mu.Lock()
	messageIDs := append([]int64(nil), p.messages...)
	texts := make(map[int64]string, len(p.messageText))
	for id, text := range p.messageText {
		texts[id] = text
	}
	questionMessages := make(map[int64]int, len(p.questionMessages))
	for id, index := range p.questionMessages {
		questionMessages[id] = index
	}
	forceReplies := make(map[int64]bool, len(p.forceReplyMessages))
	for id, isForceReply := range p.forceReplyMessages {
		forceReplies[id] = isForceReply
	}
	answers := make([]string, len(p.questions))
	for index := range p.questions {
		answers[index] = p.questions[index].answer
	}
	mainMessage := p.mainMessage
	eventType := p.event.Type
	questions := append([]model.Question(nil), p.event.Questions...)
	r.mu.Unlock()

	for _, id := range messageIDs {
		questionIndex, isQuestion := questionMessages[id]
		isForceReply := forceReplies[id]
		if !isQuestion && !isForceReply && id != mainMessage {
			continue
		}
		messageStatus := status
		if isQuestion && questionIndex >= 0 && questionIndex < len(questions) && questionIndex < len(answers) && answers[questionIndex] != "" {
			messageStatus = "Resposta: " + answers[questionIndex] + ". " + status
		}
		original := texts[id]
		updated := original + "\n\n— " + messageStatus
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if utf8.RuneCountInString(updated) <= channel.MaxMessageRunes {
			if err := r.channel.Edit(ctx, id, updated, nil); err != nil {
				r.logger.Errorf("editar mensagem Telegram: %v", err)
			}
			cancel()
			continue
		}
		if isQuestion || eventType == model.EventPermission && id == mainMessage {
			if err := r.channel.EditReplyMarkup(ctx, id, nil); err != nil {
				r.logger.Errorf("remover botões da mensagem Telegram: %v", err)
			}
		}
		if _, err := r.channel.SendReply(ctx, id, messageStatus); err != nil {
			r.logger.Errorf("enviar status da mensagem Telegram: %v", err)
		}
		cancel()
	}
}

func (r *Router) queueLate(session, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.Queues[session] = append(r.state.Queues[session], text)
	r.pruneSessionLabelsLocked()
	if err := saveState(r.home, r.state); err != nil {
		r.logger.Errorf("salvar resposta enfileirada: %v", err)
	}
}

func (r *Router) endSession(session string) {
	r.mu.Lock()
	now := time.Now()
	r.endedSessions[session] = now
	r.pruneEndedSessionsLocked(now)
	delete(r.state.Queues, session)
	for messageID, entry := range r.lateMessage {
		if entry.sessionID == session {
			delete(r.lateMessage, messageID)
		}
	}
	for label, sessionID := range r.sessionLabels {
		if sessionID == session {
			delete(r.sessionLabels, label)
		}
	}
	id := r.waiting[session]
	p := r.pending[id]
	if p != nil {
		p.completed = true
		select {
		case p.result <- model.Resolution{Action: model.ActionNone}:
		default:
		}
		delete(r.pending, id)
		delete(r.waiting, session)
		for _, messageID := range p.messages {
			delete(r.byMessage, messageID)
			delete(r.lateMessage, messageID)
		}
	}
	r.pruneSessionLabelsLocked()
	if err := saveState(r.home, r.state); err != nil {
		r.logger.Errorf("salvar estado ao encerrar sessão: %v", err)
	}
	r.mu.Unlock()
	if p != nil {
		r.editPending(p, "Sessão encerrada; solicitações pendentes canceladas.")
	}
}

func (r *Router) Away() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Away
}

func (r *Router) setAway(away bool) {
	r.mu.Lock()
	r.state.Away = away
	if err := saveState(r.home, r.state); err != nil {
		r.logger.Errorf("salvar modo ausente: %v", err)
	}
	r.mu.Unlock()
}

func (r *Router) Status() (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Away, len(r.waiting)
}

func (r *Router) SetAway(away bool) { r.setAway(away) }

func (r *Router) WaitingSessions() []model.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]model.Event, 0, len(r.waiting))
	for _, id := range r.waiting {
		if p := r.pending[id]; p != nil {
			items = append(items, p.event)
		}
	}
	return items
}

func (r *Router) waitingText() string {
	items := r.WaitingSessions()
	if len(items) == 0 {
		return "Nenhuma sessão aguardando."
	}
	var builder strings.Builder
	builder.WriteString("Sessões aguardando:")
	for _, event := range items {
		builder.WriteString("\n- ")
		builder.WriteString(string(event.Agent))
		builder.WriteString(" | ")
		builder.WriteString(event.Project)
		builder.WriteString(" | ")
		builder.WriteString(event.SessionName)
	}
	return builder.String()
}

func (r *Router) sendText(ctx context.Context, text string) {
	r.send(ctx, text, nil, false)
}

func (r *Router) send(ctx context.Context, text string, keyboard channel.Keyboard, forceReply bool) channel.SentMessage {
	message, err := r.channel.Send(ctx, text, keyboard, forceReply)
	if err != nil {
		r.logger.Errorf("enviar mensagem Telegram: %v", err)
	}
	return message
}

func (r *Router) header(event model.Event) string {
	return fmt.Sprintf("[%s · %s · %s]", event.Agent, event.Project, event.SessionName)
}

func (r *Router) questionPrompt(event model.Event, id string, index int, selected map[int]bool) (string, channel.Keyboard) {
	question := event.Questions[index]
	text := r.header(event) + "\n"
	if question.Header != "" {
		text += question.Header + "\n"
	}
	text += question.Text
	return text, questionKeyboard(id, index, question, selected)
}

func questionKeyboard(id string, index int, question model.Question, selected map[int]bool) channel.Keyboard {
	keyboard := channel.Keyboard{}
	for optionIndex, option := range question.Options {
		label := option.Label
		if question.MultiSelect {
			prefix := "[ ] "
			if selected[optionIndex] {
				prefix = "[x] "
			}
			label = prefix + label
		}
		keyboard = append(keyboard, []channel.Button{{Text: label, Data: fmt.Sprintf("q:%s:%d:%d", id, index, optionIndex)}})
	}
	keyboard = append(keyboard, []channel.Button{{Text: "Outro (texto)", Data: fmt.Sprintf("q:%s:%d:o", id, index)}})
	if question.MultiSelect {
		keyboard = append(keyboard, []channel.Button{{Text: "Confirmar", Data: fmt.Sprintf("q:%s:%d:c", id, index)}})
	}
	return keyboard
}

func selectedLabels(question model.Question, selected map[int]bool) []string {
	labels := []string{}
	for index, option := range question.Options {
		if selected[index] {
			labels = append(labels, option.Label)
		}
	}
	return labels
}

func answerQuestionWithText(question model.Question, progress *questionProgress, text string) {
	if question.MultiSelect {
		labels := selectedLabels(question, progress.selected)
		labels = append(labels, text)
		progress.answer = strings.Join(labels, ", ")
		return
	}
	progress.answer = text
}

func allQuestionsDone(questions []questionProgress) bool {
	if len(questions) == 0 {
		return false
	}
	for _, question := range questions {
		if !question.done {
			return false
		}
	}
	return true
}

func firstUnanswered(questions []questionProgress) int {
	for i, question := range questions {
		if !question.done {
			return i
		}
	}
	return -1
}

func questionReason(questions []model.Question, answers []questionProgress) string {
	parts := make([]string, 0, len(questions))
	for index, question := range questions {
		answer := "(sem resposta)"
		if index < len(answers) && answers[index].answer != "" {
			answer = answers[index].answer
		}
		parts = append(parts, fmt.Sprintf("Q%d '%s': %s", index+1, question.Text, answer))
	}
	return "O usuário está ausente e respondeu pelo celular: " + strings.Join(parts, "; ") + ". Prossiga com essas respostas e não chame a ferramenta de perguntas novamente para estas questões."
}

func (r *Router) newID() string {
	var bytes [5]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	r.mu.Lock()
	r.idCounter++
	value := r.idCounter
	r.mu.Unlock()
	return fmt.Sprintf("%010x", value)
}
