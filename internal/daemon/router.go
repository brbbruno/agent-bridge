package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
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
	message  map[string]int64
}

type pending struct {
	id                  string
	event               model.Event
	result              chan model.Resolution
	messages            []channel.MessageKey
	messageText         map[channel.MessageKey]string
	questionMessages    map[channel.MessageKey]int
	forceReplyMessages  map[channel.MessageKey]bool
	mainMessage         map[string]int64
	questions           []questionProgress
	awaitingText        string
	awaitingQuestion    int
	awaitingTextMessage channel.MessageKey
	completed           bool
	resolvedBy          string
}

type Router struct {
	mu            sync.Mutex
	home          string
	machine       string
	cfg           config.Config
	state         persistedState
	channels      []channel.Channel
	channelByName map[string]channel.Channel
	logger        *logx.Logger
	started       time.Time
	pending       map[string]*pending
	waiting       map[string]string
	byMessage     map[channel.MessageKey]string
	lateMessage   map[channel.MessageKey]lateMessageEntry
	sessionLabels map[string]string
	endedSessions map[string]time.Time
	idCounter     uint64
}

func NewRouter(home string, cfg config.Config, channels []channel.Channel, logger *logx.Logger) (*Router, error) {
	state, err := loadState(home)
	if err != nil {
		return nil, err
	}
	machine := config.MachineName(cfg)
	channels = filterChannels(channels)
	channelByName := make(map[string]channel.Channel, len(channels))
	for _, ch := range channels {
		channelByName[ch.Name()] = ch
	}
	return &Router{
		home:          home,
		machine:       machine,
		cfg:           cfg,
		state:         state,
		channels:      channels,
		channelByName: channelByName,
		logger:        logger,
		started:       time.Now(),
		pending:       map[string]*pending{},
		waiting:       map[string]string{},
		byMessage:     map[channel.MessageKey]string{},
		lateMessage:   map[channel.MessageKey]lateMessageEntry{},
		sessionLabels: map[string]string{},
		endedSessions: map[string]time.Time{},
	}, nil
}

func filterChannels(channels []channel.Channel) []channel.Channel {
	filtered := make([]channel.Channel, 0, len(channels))
	seen := map[string]bool{}
	for _, ch := range channels {
		if isNilChannel(ch) || ch.Name() == "" || seen[ch.Name()] {
			continue
		}
		seen[ch.Name()] = true
		filtered = append(filtered, ch)
	}
	return filtered
}

func isNilChannel(ch channel.Channel) bool {
	if ch == nil {
		return true
	}
	value := reflect.ValueOf(ch)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (r *Router) rememberPrompt(event model.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	changed := false
	if r.state.SessionTitles == nil {
		r.state.SessionTitles = map[string]sessionTitle{}
		changed = true
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	for session, title := range r.state.SessionTitles {
		if title.SeenAt.Before(cutoff) {
			delete(r.state.SessionTitles, session)
			changed = true
		}
	}
	if event.SessionID != "" && strings.TrimSpace(event.Prompt) != "" {
		if _, exists := r.state.SessionTitles[event.SessionID]; !exists {
			title := strings.Join(strings.Fields(event.Prompt), " ")
			runes := []rune(title)
			if len(runes) > 80 {
				title = string(runes[:79]) + "…"
			}
			if title != "" {
				r.state.SessionTitles[event.SessionID] = sessionTitle{Title: title, SeenAt: now}
				changed = true
			}
		}
	}
	if changed {
		if err := saveState(r.home, r.state); err != nil {
			r.logger.Errorf("salvar título de sessão: %v", err)
		}
	}
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
		r.rememberPrompt(event)
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
	if len(r.channels) == 0 {
		if r.logger != nil {
			r.logger.Errorf("nenhum canal configurado; hook liberado para a sessão %s", event.SessionID)
		}
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
		messageText:        map[channel.MessageKey]string{},
		questionMessages:   map[channel.MessageKey]int{},
		forceReplyMessages: map[channel.MessageKey]bool{},
		mainMessage:        map[string]int64{},
		awaitingQuestion:   -1,
	}
	if event.Type == model.EventQuestion {
		p.questions = make([]questionProgress, len(event.Questions))
		for i := range p.questions {
			p.questions[i].selected = map[int]bool{}
			p.questions[i].message = map[string]int64{}
		}
	}
	r.mu.Lock()
	r.pending[p.id] = p
	r.waiting[event.SessionID] = p.id
	r.sessionLabels[event.SessionID] = event.SessionID
	r.sessionLabels[event.SessionName] = event.SessionID
	r.mu.Unlock()

	session := r.sessionRef(event)
	if event.Type == model.EventQuestion {
		for index := range event.Questions {
			text, keyboard := r.questionPrompt(event, p.id, index, p.questions[index].selected)
			sent := false
			for _, ch := range r.channels {
				message, err := ch.Send(ctx, channel.Outgoing{Session: session, Text: text, Keyboard: keyboard})
				if err != nil {
					r.logChannelError(ch, "enviar pergunta", err)
					continue
				}
				sent = true
				r.addQuestionMessage(p, index, ch.Name(), message, text)
			}
			if !sent {
				r.finish(p, false)
				return model.Resolution{Action: model.ActionNone}
			}
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
				text += "\n```\n" + event.ToolSummary + "\n```"
			}
		}
		keyboard := channel.Keyboard(nil)
		if event.Type == model.EventPermission {
			keyboard = channel.Keyboard{{
				{Text: "Aprovar", Data: "p:" + p.id + ":a", Style: "success"},
				{Text: "Negar", Data: "p:" + p.id + ":d", Style: "danger"},
				{Text: "Negar com instrução", Data: "p:" + p.id + ":i"},
			}}
		}
		sent := false
		for _, ch := range r.channels {
			message, err := ch.Send(ctx, channel.Outgoing{Session: session, Text: text, Keyboard: keyboard})
			if err != nil {
				r.logChannelError(ch, "enviar solicitação", err)
				continue
			}
			sent = true
			r.addMainMessage(p, ch.Name(), message, text)
		}
		if !sent {
			r.finish(p, false)
			return model.Resolution{Action: model.ActionNone}
		}
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
		origin := r.resolvedBy(p)
		r.editPending(p, resolutionStatus(event.Type, result, origin))
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
	if len(r.channels) == 0 {
		return
	}
	text := r.header(event)
	switch event.Type {
	case model.EventStop:
		text += "\n" + event.Message
	case model.EventPermission:
		text += "\nPrecisa de aprovação: " + event.ToolName
		if event.ToolSummary != "" {
			text += "\n```\n" + event.ToolSummary + "\n```"
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
	session := r.sessionRef(event)
	for _, ch := range r.channels {
		go func(ch channel.Channel) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			message, err := ch.Send(ctx, channel.Outgoing{Session: session, Text: text})
			if err != nil {
				r.logChannelError(ch, "enviar notificação", err)
				return
			}
			r.mu.Lock()
			if _, ended := r.endedSessions[event.SessionID]; ended {
				r.mu.Unlock()
				return
			}
			r.addLateMessagesLocked(messageKeys(ch.Name(), message), event.SessionID, time.Now())
			r.sessionLabels[event.SessionID] = event.SessionID
			r.sessionLabels[event.SessionName] = event.SessionID
			r.pruneSessionLabelsLocked()
			r.mu.Unlock()
		}(ch)
	}
}

func (r *Router) HandleUpdate(ctx context.Context, update channel.Update) {
	if r.channelFor(update) == nil || update.Ignored {
		return
	}
	if update.CallbackID != "" && strings.TrimSpace(update.Text) == "" && update.TextToken == "" {
		r.handleCallback(ctx, update)
		return
	}
	if strings.TrimSpace(update.Text) != "" || update.TextToken != "" {
		r.handleText(ctx, update)
	}
}

func (r *Router) handleCallback(ctx context.Context, update channel.Update) {
	parts := strings.Split(update.CallbackData, ":")
	if len(parts) == 0 {
		_ = r.answerCallback(ctx, update, "Ação inválida.")
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
	_ = r.answerCallback(ctx, update, "Ação inválida.")
}

func (r *Router) handlePermissionCallback(ctx context.Context, update channel.Update, id, action string) {
	r.mu.Lock()
	p := r.pending[id]
	if p == nil || p.completed || p.event.Type != model.EventPermission {
		r.mu.Unlock()
		_ = r.answerCallback(ctx, update, "Esta decisão expirou.")
		return
	}
	r.mu.Unlock()
	switch action {
	case "a":
		_ = r.answerCallback(ctx, update, "Resposta recebida.")
		r.deliver(p, model.Resolution{Action: model.ActionApprove}, update.Channel)
	case "d":
		_ = r.answerCallback(ctx, update, "Resposta recebida.")
		r.deliver(p, model.Resolution{Action: model.ActionDeny, Reason: "O usuário negou a aprovação pelo " + update.Channel + "."}, update.Channel)
	case "i":
		r.mu.Lock()
		p.awaitingText = "permission"
		p.awaitingQuestion = -1
		r.mu.Unlock()
		prompt := "Digite a instrução para negar a ação:"
		ch := r.channelFor(update)
		message, err := ch.RequestText(ctx, update, r.sessionRef(p.event), prompt, "t:"+p.id)
		if err != nil {
			r.logChannelError(ch, "pedir instrução de negação", err)
			return
		}
		r.addForceReply(p, update.Channel, message, prompt)
	default:
		_ = r.answerCallback(ctx, update, "Ação inválida.")
	}
}

func (r *Router) handleQuestionCallback(ctx context.Context, update channel.Update, id, questionText, action string) {
	questionIndex, err := strconv.Atoi(questionText)
	if err != nil {
		_ = r.answerCallback(ctx, update, "Pergunta inválida.")
		return
	}
	r.mu.Lock()
	p := r.pending[id]
	if p == nil || p.completed || p.event.Type != model.EventQuestion || questionIndex < 0 || questionIndex >= len(p.questions) {
		r.mu.Unlock()
		_ = r.answerCallback(ctx, update, "Esta pergunta expirou.")
		return
	}
	question := p.event.Questions[questionIndex]
	progress := &p.questions[questionIndex]
	if action == "o" {
		p.awaitingText = "question"
		p.awaitingQuestion = questionIndex
		r.mu.Unlock()
		prompt := "Outro (texto) — responda à pergunta: " + question.Text
		ch := r.channelFor(update)
		message, err := ch.RequestText(ctx, update, r.sessionRef(p.event), prompt, "t:"+p.id)
		if err != nil {
			r.logChannelError(ch, "pedir resposta livre", err)
			return
		}
		r.addForceReply(p, update.Channel, message, prompt)
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
			_ = r.answerCallback(ctx, update, "Opção inválida.")
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
	selected := make(map[int]bool, len(progress.selected))
	for index, isSelected := range progress.selected {
		selected[index] = isSelected
	}
	messageIDs := make(map[string]int64, len(progress.message))
	for name, messageID := range progress.message {
		messageIDs[name] = messageID
	}
	event := p.event
	questions := append([]model.Question(nil), p.event.Questions...)
	answers := append([]questionProgress(nil), p.questions...)
	r.mu.Unlock()
	_ = r.answerCallback(ctx, update, "Resposta registrada.")
	if !allDone {
		text, _ := r.questionPrompt(event, id, questionIndex, selected)
		for name, messageID := range messageIDs {
			if ch := r.channelByName[name]; ch != nil {
				if err := ch.Edit(ctx, messageID, text, updatedKeyboard); err != nil {
					r.logChannelError(ch, "atualizar opções de pergunta", err)
				}
			}
		}
	}
	if allDone {
		r.deliver(p, model.Resolution{Action: model.ActionBlock, Reason: questionReason(questions, answers, update.Channel, update.Private)}, update.Channel)
	}
}

func (r *Router) handleRouteCallback(ctx context.Context, update channel.Update, id string) {
	r.mu.Lock()
	p := r.pending[id]
	if p == nil || p.completed {
		r.mu.Unlock()
		_ = r.answerCallback(ctx, update, "Esta sessão não está mais aguardando.")
		return
	}
	p.awaitingText = "route"
	r.mu.Unlock()
	prompt := "Digite a mensagem para a sessão selecionada:"
	ch := r.channelFor(update)
	message, err := ch.RequestText(ctx, update, r.sessionRef(p.event), prompt, "t:"+p.id)
	if err != nil {
		r.logChannelError(ch, "pedir mensagem para sessão", err)
		return
	}
	r.addForceReply(p, update.Channel, message, prompt)
}

func (r *Router) handleText(ctx context.Context, update channel.Update) {
	text := strings.TrimSpace(update.Text)
	if strings.HasPrefix(text, "/") && r.handleCommand(ctx, update, text) {
		return
	}
	if strings.HasPrefix(update.TextToken, "t:") {
		r.mu.Lock()
		p := r.pending[strings.TrimPrefix(update.TextToken, "t:")]
		r.mu.Unlock()
		if p != nil {
			r.answerPendingText(ctx, p, update, text)
		}
		return
	}
	if update.SessionID != "" {
		r.mu.Lock()
		p := r.pending[r.waiting[update.SessionID]]
		r.mu.Unlock()
		if p != nil {
			r.answerPendingText(ctx, p, update, text)
			return
		}
		r.queueLate(update.SessionID, text)
		r.reply(ctx, update, "Enfileirado; será entregue na próxima parada desta sessão.", r.sessionRefForSession(update.SessionID))
		return
	}
	if update.ReplyToMessage != 0 {
		key := channel.MessageKey{Channel: update.Channel, ID: update.ReplyToMessage}
		r.mu.Lock()
		id := r.byMessage[key]
		p := r.pending[id]
		late := r.lateMessage[key]
		session := late.sessionID
		r.mu.Unlock()
		if p != nil {
			r.answerPendingText(ctx, p, update, text)
			return
		}
		if session != "" {
			r.queueLate(session, text)
			r.reply(ctx, update, "Enfileirado; será entregue na próxima parada desta sessão.", r.sessionRefForSession(session))
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
		r.answerPendingText(ctx, waiting[0], update, text)
		return
	}
	if len(waiting) > 1 {
		keyboard := channel.Keyboard{}
		for _, p := range waiting {
			keyboard = append(keyboard, []channel.Button{{Text: string(p.event.Agent) + " | " + p.event.Project + " | " + p.event.SessionName, Data: "r:" + p.id}})
		}
		r.replyOutgoing(ctx, update, channel.Outgoing{Text: "Escolha a sessão que receberá sua mensagem:", Keyboard: keyboard})
		return
	}
	r.reply(ctx, update, "Não há sessão aguardando. Responda à mensagem do agente ou use /s <sessão> <texto> para enfileirar.")
}

func (r *Router) handleCommand(ctx context.Context, update channel.Update, text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return true
	}
	switch fields[0] {
	case "/away":
		r.setAway(true)
		r.reply(ctx, update, "Modo ausente ativado.")
	case "/back":
		r.setAway(false)
		r.reply(ctx, update, "Modo ausente desativado.")
	case "/list":
		r.reply(ctx, update, r.waitingText())
	case "/status":
		away, count := r.Status()
		mode := "no computador"
		if away {
			mode = "ausente"
		}
		r.reply(ctx, update, fmt.Sprintf("Modo: %s\nDaemon ativo há: %s\nSessões aguardando: %d", mode, time.Since(r.started).Round(time.Second), count))
	case "/help":
		r.reply(ctx, update, "Comandos: /list, /status, /away, /back, /s <sessão> <texto>. Responda às mensagens do agente para direcionar uma resposta.")
	case "/s":
		if len(fields) < 3 {
			r.reply(ctx, update, "Uso: /s <sessão> <texto>")
			return true
		}
		label := fields[1]
		message := strings.TrimSpace(strings.TrimPrefix(text, fields[0]+" "+label))
		r.mu.Lock()
		session := r.sessionLabels[label]
		p := r.pending[r.waiting[session]]
		r.mu.Unlock()
		if session == "" {
			r.reply(ctx, update, "Sessão não encontrada. Use /list para ver as sessões.")
		} else if p != nil {
			r.routeTextToPending(ctx, p, update, message)
		} else {
			r.queueLate(session, message)
			r.reply(ctx, update, "Enfileirado; será entregue na próxima parada desta sessão.", r.sessionRefForSession(session))
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
	tokenReply := update.TextToken == "t:"+p.id
	messageKey := channel.MessageKey{Channel: update.Channel, ID: update.ReplyToMessage}
	validReply := tokenReply || p.awaitingTextMessage.ID == 0 || messageKey == p.awaitingTextMessage
	if mode == "" || !validReply {
		r.mu.Unlock()
		r.routeTextToPending(ctx, p, update, text)
		return
	}
	p.awaitingText = ""
	p.awaitingTextMessage = channel.MessageKey{}
	switch mode {
	case "permission":
		r.mu.Unlock()
		r.deliver(p, model.Resolution{Action: model.ActionDeny, Reason: userAnswerPrefix(update) + text}, update.Channel)
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
			r.deliver(p, model.Resolution{Action: model.ActionBlock, Reason: questionReason(questions, answers, update.Channel, update.Private)}, update.Channel)
		} else {
			r.reply(ctx, update, "Resposta registrada. Ainda aguardando outras perguntas.", r.sessionRef(p.event))
		}
	case "route":
		r.mu.Unlock()
		r.routeTextToPending(ctx, p, update, text)
	default:
		r.mu.Unlock()
		r.routeTextToPending(ctx, p, update, text)
	}
}

func (r *Router) routeTextToPending(ctx context.Context, p *pending, update channel.Update, text string) {
	switch p.event.Type {
	case model.EventStop:
		r.deliver(p, model.Resolution{Action: model.ActionBlock, Reason: mobilePrefix + text}, update.Channel)
	case model.EventPermission:
		r.deliver(p, model.Resolution{Action: model.ActionDeny, Reason: userAnswerPrefix(update) + text}, update.Channel)
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
			r.deliver(p, model.Resolution{Action: model.ActionBlock, Reason: questionReason(questions, answers, update.Channel, update.Private)}, update.Channel)
		} else {
			r.reply(ctx, update, "Resposta registrada. Ainda aguardando outras perguntas.", r.sessionRef(p.event))
		}
	}
}

func (r *Router) deliver(p *pending, result model.Resolution, origin string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.pending[p.id]
	if current == nil || current.completed {
		return
	}
	current.completed = true
	current.resolvedBy = origin
	select {
	case current.result <- result:
	default:
	}
}

func (r *Router) resolvedBy(p *pending) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return p.resolvedBy
}

func (r *Router) finish(p *pending, keepLate bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := r.pending[p.id] != nil
	delete(r.pending, p.id)
	if r.waiting[p.event.SessionID] == p.id {
		delete(r.waiting, p.event.SessionID)
	}
	for _, key := range p.messages {
		delete(r.byMessage, key)
	}
	if keepLate && active {
		r.addLateMessagesLocked(p.messages, p.event.SessionID, time.Now())
	}
	r.pruneSessionLabelsLocked()
}

func resolutionStatus(eventType model.EventType, resolution model.Resolution, origin string) string {
	via := ""
	if origin != "" {
		via = " via " + origin
	}
	if eventType == model.EventPermission {
		if resolution.Action == model.ActionApprove {
			return "Aprovado pelo usuário" + via + "; decisão enviada ao agente."
		}
		if resolution.Action == model.ActionDeny {
			return "Negado pelo usuário" + via + "; decisão enviada ao agente."
		}
	}
	return "Resposta recebida" + via + "; será entregue ao agente."
}

func (r *Router) addMainMessage(p *pending, name string, message channel.SentMessage, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordMessageLocked(p, name, message, text)
	p.mainMessage[name] = message.ID
	if p.mainMessage[name] == 0 && len(message.IDs) > 0 {
		p.mainMessage[name] = message.IDs[len(message.IDs)-1]
	}
}

func (r *Router) addForceReply(p *pending, name string, message channel.SentMessage, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p.awaitingTextMessage = channel.MessageKey{Channel: name, ID: message.ID}
	r.recordMessageLocked(p, name, message, text)
	if message.ID != 0 {
		p.forceReplyMessages[channel.MessageKey{Channel: name, ID: message.ID}] = true
	}
}

func (r *Router) addQuestionMessage(p *pending, index int, name string, message channel.SentMessage, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordMessageLocked(p, name, message, text)
	if index >= 0 && index < len(p.questions) {
		p.questions[index].message[name] = message.ID
		if message.ID != 0 {
			p.questionMessages[channel.MessageKey{Channel: name, ID: message.ID}] = index
		}
	}
}

func (r *Router) recordMessageLocked(p *pending, name string, message channel.SentMessage, fallbackText string) {
	ids := message.IDs
	if len(ids) == 0 && message.ID != 0 {
		ids = []int64{message.ID}
	}
	for index, id := range ids {
		if id == 0 {
			continue
		}
		key := channel.MessageKey{Channel: name, ID: id}
		text := fallbackText
		if index < len(message.Chunks) {
			text = message.Chunks[index]
		}
		p.messages = append(p.messages, key)
		p.messageText[key] = text
		r.byMessage[key] = p.id
	}
}

func (r *Router) editPending(p *pending, status string) {
	r.mu.Lock()
	messageKeys := append([]channel.MessageKey(nil), p.messages...)
	texts := make(map[channel.MessageKey]string, len(p.messageText))
	for key, text := range p.messageText {
		texts[key] = text
	}
	questionMessages := make(map[channel.MessageKey]int, len(p.questionMessages))
	for key, index := range p.questionMessages {
		questionMessages[key] = index
	}
	forceReplies := make(map[channel.MessageKey]bool, len(p.forceReplyMessages))
	for key, isForceReply := range p.forceReplyMessages {
		forceReplies[key] = isForceReply
	}
	answers := make([]string, len(p.questions))
	for index := range p.questions {
		answers[index] = p.questions[index].answer
	}
	mainMessages := make(map[string]int64, len(p.mainMessage))
	for name, id := range p.mainMessage {
		mainMessages[name] = id
	}
	eventType := p.event.Type
	event := p.event
	questions := append([]model.Question(nil), p.event.Questions...)
	r.mu.Unlock()
	session := r.sessionRef(event)

	for _, key := range messageKeys {
		ch := r.channelByName[key.Channel]
		if ch == nil {
			continue
		}
		questionIndex, isQuestion := questionMessages[key]
		isForceReply := forceReplies[key]
		isMain := key.ID == mainMessages[key.Channel]
		if !isQuestion && !isForceReply && !isMain {
			continue
		}
		messageStatus := status
		if isQuestion && questionIndex >= 0 && questionIndex < len(questions) && questionIndex < len(answers) && answers[questionIndex] != "" {
			messageStatus = "Resposta: " + answers[questionIndex] + ". " + status
		}
		original := texts[key]
		updated := original + "\n\n— " + messageStatus
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		limit := ch.MessageLimit()
		if limit <= 0 {
			limit = channel.MaxMessageRunes
		}
		if utf8.RuneCountInString(updated) <= limit {
			if err := ch.Edit(ctx, key.ID, updated, nil); err != nil {
				r.logChannelError(ch, "editar mensagem", err)
			}
			cancel()
			continue
		}
		if isQuestion || eventType == model.EventPermission && isMain {
			if err := ch.EditReplyMarkup(ctx, key.ID, nil); err != nil {
				r.logChannelError(ch, "remover botões da mensagem", err)
			}
		}
		if _, err := ch.Send(ctx, channel.Outgoing{Session: session, ReplyTo: key.ID, Text: messageStatus}); err != nil {
			r.logChannelError(ch, "enviar status da mensagem", err)
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
	delete(r.state.SessionTitles, session)
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

func (r *Router) channelFor(update channel.Update) channel.Channel {
	return r.channelByName[update.Channel]
}

func (r *Router) answerCallback(ctx context.Context, update channel.Update, text string) error {
	ch := r.channelFor(update)
	if ch == nil {
		return nil
	}
	return ch.AnswerCallback(ctx, update, text)
}

func (r *Router) reply(ctx context.Context, update channel.Update, text string, session ...channel.SessionRef) channel.SentMessage {
	message := channel.Outgoing{Text: text}
	if len(session) > 0 {
		message.Session = session[0]
	}
	return r.replyOutgoing(ctx, update, message)
}

func (r *Router) replyOutgoing(ctx context.Context, update channel.Update, message channel.Outgoing) channel.SentMessage {
	message.Origin = &update
	ch := r.channelFor(update)
	if ch == nil {
		return channel.SentMessage{}
	}
	sent, err := ch.Send(ctx, message)
	if err != nil {
		r.logChannelError(ch, "enviar resposta", err)
	}
	return sent
}

func (r *Router) logChannelError(ch channel.Channel, action string, err error) {
	if r.logger != nil {
		name := ""
		if ch != nil {
			name = ch.Name()
		}
		r.logger.Errorf("%s %s: %v", action, name, err)
	}
}

func (r *Router) sessionRef(event model.Event) channel.SessionRef {
	return channel.SessionRef{ID: event.SessionID, Name: event.SessionName, Title: r.sessionTitle(event), Project: event.Project, Agent: string(event.Agent)}
}

func (r *Router) sessionTitle(event model.Event) string {
	if title := strings.TrimSpace(event.SessionTitle); title != "" {
		return title
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(r.state.SessionTitles[event.SessionID].Title)
}

func (r *Router) sessionRefForSession(session string) channel.SessionRef {
	r.mu.Lock()
	p := r.pending[r.waiting[session]]
	title := strings.TrimSpace(r.state.SessionTitles[session].Title)
	r.mu.Unlock()
	if p != nil {
		return r.sessionRef(p.event)
	}
	return channel.SessionRef{ID: session, Name: session, Title: title}
}

func messageKeys(name string, message channel.SentMessage) []channel.MessageKey {
	ids := message.IDs
	if len(ids) == 0 && message.ID != 0 {
		ids = []int64{message.ID}
	}
	keys := make([]channel.MessageKey, 0, len(ids))
	for _, id := range ids {
		if id != 0 {
			keys = append(keys, channel.MessageKey{Channel: name, ID: id})
		}
	}
	return keys
}

func userAnswerPrefix(update channel.Update) string {
	if update.Private {
		return mobilePrefix
	}
	return "Mensagem do usuário (via " + update.Channel + "): "
}

func (r *Router) header(event model.Event) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headerLocked(event)
}

func (r *Router) headerLocked(event model.Event) string {
	agent := string(event.Agent)
	switch event.Agent {
	case model.AgentDevin:
		agent = "Devin"
	case model.AgentClaude:
		agent = "Claude"
	}
	header := fmt.Sprintf("%s · %s · %s", agent, r.machine, event.Project)
	title := strings.TrimSpace(event.SessionTitle)
	if title == "" {
		title = strings.TrimSpace(r.state.SessionTitles[event.SessionID].Title)
	}
	if title != "" {
		return fmt.Sprintf("**%s**\nSessão: %s (%s)\n", header, title, event.SessionName)
	}
	return fmt.Sprintf("**%s**\nSessão: %s\n", header, event.SessionName)
}

func (r *Router) questionPrompt(event model.Event, id string, index int, selected map[int]bool) (string, channel.Keyboard) {
	question := event.Questions[index]
	text := r.header(event) + "\n"
	if question.Header != "" {
		text += "**" + question.Header + "**\n"
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

func questionReason(questions []model.Question, answers []questionProgress, origin string, private bool) string {
	parts := make([]string, 0, len(questions))
	for index, question := range questions {
		answer := "(sem resposta)"
		if index < len(answers) && answers[index].answer != "" {
			answer = answers[index].answer
		}
		parts = append(parts, fmt.Sprintf("Q%d '%s': %s", index+1, question.Text, answer))
	}
	via := "via " + origin
	if private {
		via = "pelo celular"
	}
	return "O usuário está ausente e respondeu " + via + ": " + strings.Join(parts, "; ") + ". Prossiga com essas respostas e não chame a ferramenta de perguntas novamente para estas questões."
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
