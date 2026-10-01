package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/model"
)

type progressCategory uint8

const (
	progressOther progressCategory = iota
	progressCommands
	progressEdits
	progressReads
	progressSearches
)

type progressCounts struct {
	edits    int
	commands int
	reads    int
	searches int
	others   int
}

type progressAction struct {
	category progressCategory
	toolName string
	summary  string
	failed   bool
}

type progressPanel struct {
	turnID     string
	session    channel.SessionRef
	counts     progressCounts
	failures   int
	recent     []progressAction
	startedAt  time.Time
	lastFlush  time.Time
	finished   bool
	timer      *time.Timer
	inFlight   bool
	dirty      bool
	cancel     context.CancelFunc
	messageIDs map[string]int64
}

type progressSnapshot struct {
	session    channel.SessionRef
	text       string
	messageIDs map[string]int64
	channels   []channel.Channel
}

func (r *Router) handleProgress(event model.Event) model.Resolution {
	channels := r.progressChannels()
	if len(channels) == 0 {
		return model.Resolution{Action: model.ActionNone}
	}
	session := r.sessionRef(event)
	now := time.Now()
	r.mu.Lock()
	panel := r.progressPanels[event.SessionID]
	if panel != nil && (panel.finished || panel.turnID != "" && event.TurnID != "" && panel.turnID != event.TurnID) {
		r.clearProgressPanelLocked(event.SessionID, panel)
		panel = nil
	}
	if panel == nil {
		panel = &progressPanel{
			turnID:     event.TurnID,
			session:    session,
			startedAt:  now,
			messageIDs: map[string]int64{},
		}
		r.progressPanels[event.SessionID] = panel
	} else {
		if panel.turnID == "" {
			panel.turnID = event.TurnID
		}
		panel.session = session
	}
	category := classifyProgressTool(event.ToolName)
	switch category {
	case progressCommands:
		panel.counts.commands++
	case progressEdits:
		panel.counts.edits++
	case progressReads:
		panel.counts.reads++
	case progressSearches:
		panel.counts.searches++
	default:
		panel.counts.others++
	}
	if event.ToolFailed {
		panel.failures++
	}
	panel.recent = append(panel.recent, progressAction{category: category, toolName: event.ToolName, summary: event.ToolSummary, failed: event.ToolFailed})
	if len(panel.recent) > 6 {
		panel.recent = append([]progressAction(nil), panel.recent[len(panel.recent)-6:]...)
	}
	if panel.inFlight {
		panel.dirty = true
		r.mu.Unlock()
		return model.Resolution{Action: model.ActionNone}
	}
	if panel.timer != nil {
		r.mu.Unlock()
		return model.Resolution{Action: model.ActionNone}
	}
	if panel.lastFlush.IsZero() {
		r.beginProgressFlushLocked(event.SessionID, panel, channels)
	} else {
		delay := r.progressInterval - time.Since(panel.lastFlush)
		r.scheduleProgressFlushLocked(event.SessionID, panel, channels, delay)
	}
	r.mu.Unlock()
	return model.Resolution{Action: model.ActionNone}
}

func (r *Router) progressChannels() []channel.Channel {
	channels := make([]channel.Channel, 0, len(r.channels))
	for _, ch := range r.channels {
		capable, ok := ch.(channel.ProgressCapable)
		if ok && capable.SupportsProgress() {
			channels = append(channels, ch)
		}
	}
	return channels
}

func (r *Router) beginProgressFlushLocked(sessionID string, panel *progressPanel, channels []channel.Channel) {
	panel.inFlight = true
	panel.dirty = false
	snapshot := r.progressSnapshotLocked(panel, channels)
	go r.flushProgress(sessionID, panel, snapshot)
}

func (r *Router) scheduleProgressFlushLocked(sessionID string, panel *progressPanel, channels []channel.Channel, delay time.Duration) {
	if delay <= 0 {
		r.beginProgressFlushLocked(sessionID, panel, channels)
		return
	}
	panel.timer = time.AfterFunc(delay, func() { r.progressTimerFired(sessionID, panel, channels) })
}

func (r *Router) progressTimerFired(sessionID string, panel *progressPanel, channels []channel.Channel) {
	r.mu.Lock()
	if r.progressPanels[sessionID] != panel || panel.timer == nil {
		r.mu.Unlock()
		return
	}
	panel.timer = nil
	if panel.inFlight {
		panel.dirty = true
		r.mu.Unlock()
		return
	}
	r.beginProgressFlushLocked(sessionID, panel, channels)
	r.mu.Unlock()
}

func (r *Router) progressSnapshotLocked(panel *progressPanel, channels []channel.Channel) progressSnapshot {
	ids := make(map[string]int64, len(panel.messageIDs))
	for name, id := range panel.messageIDs {
		ids[name] = id
	}
	return progressSnapshot{session: panel.session, text: r.progressTextLocked(panel, time.Now()), messageIDs: ids, channels: append([]channel.Channel(nil), channels...)}
}

func (r *Router) flushProgress(sessionID string, panel *progressPanel, snapshot progressSnapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	r.mu.Lock()
	if r.progressPanels[sessionID] != panel {
		r.mu.Unlock()
		cancel()
		return
	}
	panel.cancel = cancel
	r.mu.Unlock()
	for _, ch := range snapshot.channels {
		if ctx.Err() != nil {
			break
		}
		messageID := snapshot.messageIDs[ch.Name()]
		if messageID == 0 {
			message, err := ch.Send(ctx, channel.Outgoing{Session: snapshot.session, Text: snapshot.text})
			if err != nil {
				r.logChannelError(ch, "enviar painel de andamento", err)
				continue
			}
			messageID = message.ID
			if messageID == 0 && len(message.IDs) > 0 {
				messageID = message.IDs[len(message.IDs)-1]
			}
			if messageID != 0 {
				r.mu.Lock()
				if r.progressPanels[sessionID] == panel {
					panel.messageIDs[ch.Name()] = messageID
				}
				r.mu.Unlock()
			}
			continue
		}
		if err := ch.Edit(ctx, messageID, snapshot.text, nil); err != nil {
			r.logChannelError(ch, "atualizar painel de andamento", err)
		}
	}
	cancel()
	r.mu.Lock()
	if r.progressPanels[sessionID] != panel {
		r.mu.Unlock()
		return
	}
	panel.cancel = nil
	panel.inFlight = false
	panel.lastFlush = time.Now()
	if panel.dirty {
		if panel.finished {
			r.beginProgressFlushLocked(sessionID, panel, snapshot.channels)
		} else {
			r.scheduleProgressFlushLocked(sessionID, panel, snapshot.channels, r.progressInterval)
		}
	}
	r.mu.Unlock()
}

func (r *Router) finishProgressPanel(event model.Event) {
	channels := r.progressChannels()
	if len(channels) == 0 {
		return
	}
	r.mu.Lock()
	panel := r.progressPanels[event.SessionID]
	if panel == nil || panel.finished {
		r.mu.Unlock()
		return
	}
	panel.finished = true
	if panel.timer != nil {
		panel.timer.Stop()
		panel.timer = nil
	}
	if panel.inFlight {
		panel.dirty = true
		r.mu.Unlock()
		return
	}
	r.beginProgressFlushLocked(event.SessionID, panel, channels)
	r.mu.Unlock()
}

func (r *Router) dropStaleProgressPanel(sessionID string) {
	r.mu.Lock()
	panel := r.progressPanels[sessionID]
	if panel != nil && !panel.finished {
		r.clearProgressPanelLocked(sessionID, panel)
	}
	r.mu.Unlock()
}

func (r *Router) clearProgressPanel(sessionID string) {
	r.mu.Lock()
	if panel := r.progressPanels[sessionID]; panel != nil {
		r.clearProgressPanelLocked(sessionID, panel)
	}
	r.mu.Unlock()
}

func (r *Router) clearProgressPanelLocked(sessionID string, panel *progressPanel) {
	if panel.timer != nil {
		panel.timer.Stop()
		panel.timer = nil
	}
	if panel.cancel != nil {
		panel.cancel()
		panel.cancel = nil
	}
	delete(r.progressPanels, sessionID)
}

func classifyProgressTool(name string) progressCategory {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "exec", "bash":
		return progressCommands
	case "edit", "write", "multiedit", "notebook_edit", "notebookedit":
		return progressEdits
	case "read", "notebook_read":
		return progressReads
	case "grep", "glob", "find_file_by_name", "code_search", "web_search", "websearch", "webfetch":
		return progressSearches
	default:
		return progressOther
	}
}

func (r *Router) progressTextLocked(panel *progressPanel, now time.Time) string {
	state := "em andamento"
	if panel.finished {
		state = "concluído em " + now.Sub(panel.startedAt).Round(time.Second).String()
	}
	total := panel.counts.edits + panel.counts.commands + panel.counts.reads + panel.counts.searches + panel.counts.others
	text := "**Andamento do turno** · " + state + "\n" + progressCount(total, "ação", "ações")
	categories := make([]string, 0, 5)
	if panel.counts.edits > 0 {
		categories = append(categories, progressCount(panel.counts.edits, "edição", "edições"))
	}
	if panel.counts.commands > 0 {
		categories = append(categories, progressCount(panel.counts.commands, "comando", "comandos"))
	}
	if panel.counts.reads > 0 {
		categories = append(categories, progressCount(panel.counts.reads, "leitura", "leituras"))
	}
	if panel.counts.searches > 0 {
		categories = append(categories, progressCount(panel.counts.searches, "busca", "buscas"))
	}
	if panel.counts.others > 0 {
		categories = append(categories, progressCount(panel.counts.others, "outra", "outras"))
	}
	if len(categories) > 0 {
		text += ": " + strings.Join(categories, ", ")
	}
	if panel.failures > 0 {
		text += " · " + progressCount(panel.failures, "falha", "falhas")
	}
	if r.cfg.ProgressDetail == "completo" && len(panel.recent) > 0 {
		text += "\nÚltimas ações:"
		for _, action := range panel.recent {
			text += "\n- " + formatProgressAction(action)
		}
	}
	return text
}

func progressCount(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func formatProgressAction(action progressAction) string {
	name := strings.TrimSpace(action.summary)
	if name == "" {
		name = strings.TrimSpace(action.toolName)
	}
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		name = "ferramenta"
	}
	name = strings.ReplaceAll(name, "`", "'")
	inline := "`" + name + "`"
	var line string
	switch action.category {
	case progressEdits:
		line = "editou " + inline
	case progressReads:
		line = "leu " + inline
	case progressSearches:
		line = "buscou " + inline
	default:
		line = inline
	}
	if action.failed {
		line = "falhou: " + line
	}
	return line
}
