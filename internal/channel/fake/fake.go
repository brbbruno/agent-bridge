package fake

import (
	"context"
	"errors"
	"sync"

	"github.com/brbbruno/agent-bridge/internal/channel"
)

type SentRecord struct {
	ID          int64
	Text        string
	Keyboard    channel.Keyboard
	ForceReply  bool
	ReplyTo     int64
	Session     channel.SessionRef
	TextRequest bool
	Token       string
}

type EditRecord struct {
	ID         int64
	Text       string
	Keyboard   channel.Keyboard
	MarkupOnly bool
}

type Fake struct {
	mu            sync.Mutex
	nextID        int64
	name          string
	limit         int
	SendError     error
	Sent          []SentRecord
	Edits         []EditRecord
	Callbacks     []string
	CallbackTexts []string
	Actions       []string
	UpdatesCh     chan channel.Update
}

func New() *Fake { return NewNamed("Telegram", channel.MaxMessageRunes) }

func NewNamed(name string, limit int) *Fake {
	if name == "" {
		name = "Telegram"
	}
	if limit <= 0 {
		limit = channel.MaxMessageRunes
	}
	return &Fake{name: name, limit: limit, UpdatesCh: make(chan channel.Update, 32)}
}

func (f *Fake) Name() string { return f.name }

func (f *Fake) MessageLimit() int { return f.limit }

func (f *Fake) Run(ctx context.Context, handle func(context.Context, channel.Update)) error {
	if handle == nil {
		return errors.New("handler de atualização ausente")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case update := <-f.UpdatesCh:
			if update.Channel == "" {
				update.Channel = f.Name()
			}
			handle(ctx, update)
		}
	}
}

func (f *Fake) Send(_ context.Context, message channel.Outgoing) (channel.SentMessage, error) {
	return f.send(message, false, false, "")
}

func (f *Fake) RequestText(ctx context.Context, update channel.Update, session channel.SessionRef, prompt, token string) (channel.SentMessage, error) {
	f.mu.Lock()
	f.Actions = append(f.Actions, "request-text")
	f.mu.Unlock()
	message := channel.Outgoing{Session: session, Text: prompt}
	if f.Name() == "Telegram" {
		if err := f.AnswerCallback(ctx, update, "Responda em seguida."); err != nil {
			return channel.SentMessage{}, err
		}
		return f.send(message, true, true, token)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SendError != nil {
		return channel.SentMessage{}, f.SendError
	}
	f.Sent = append(f.Sent, SentRecord{Text: prompt, Session: session, TextRequest: true, Token: token})
	return channel.SentMessage{}, nil
}

func (f *Fake) send(message channel.Outgoing, forceReply, textRequest bool, token string) (channel.SentMessage, error) {
	chunks := splitText(message.Text, f.MessageLimit())
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SendError != nil {
		return channel.SentMessage{}, f.SendError
	}
	ids := make([]int64, 0, len(chunks))
	for index, chunk := range chunks {
		f.nextID++
		ids = append(ids, f.nextID)
		markup := channel.Keyboard(nil)
		force := false
		if index == len(chunks)-1 {
			markup = message.Keyboard
			force = forceReply
		}
		f.Sent = append(f.Sent, SentRecord{ID: f.nextID, Text: chunk, Keyboard: cloneKeyboard(markup), ForceReply: force, ReplyTo: message.ReplyTo, Session: message.Session, TextRequest: textRequest, Token: token})
	}
	return channel.SentMessage{ID: ids[len(ids)-1], IDs: ids, Chunks: chunks}, nil
}

func (f *Fake) Edit(_ context.Context, id int64, text string, keyboard channel.Keyboard) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Edits = append(f.Edits, EditRecord{ID: id, Text: text, Keyboard: cloneKeyboard(keyboard)})
	return nil
}

func (f *Fake) EditReplyMarkup(_ context.Context, id int64, keyboard channel.Keyboard) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Edits = append(f.Edits, EditRecord{ID: id, Keyboard: cloneKeyboard(keyboard), MarkupOnly: true})
	return nil
}

func (f *Fake) AnswerCallback(_ context.Context, update channel.Update, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Callbacks = append(f.Callbacks, update.CallbackID)
	f.CallbackTexts = append(f.CallbackTexts, text)
	f.Actions = append(f.Actions, "answer-callback")
	return nil
}

func (f *Fake) Updates(ctx context.Context, _ int64, _ int) ([]channel.Update, error) {
	select {
	case update := <-f.UpdatesCh:
		return []channel.Update{update}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *Fake) Push(update channel.Update) { f.UpdatesCh <- update }

func (f *Fake) Snapshot() ([]SentRecord, []EditRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sent := append([]SentRecord(nil), f.Sent...)
	edits := append([]EditRecord(nil), f.Edits...)
	return sent, edits
}

func (f *Fake) CallbackSnapshot() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	callbacks := append([]string(nil), f.Callbacks...)
	texts := append([]string(nil), f.CallbackTexts...)
	return callbacks, texts
}

func (f *Fake) ActionSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Actions...)
}

func splitText(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	chunks := make([]string, 0, len(runes)/limit+1)
	for len(runes) > limit {
		chunks = append(chunks, string(runes[:limit]))
		runes = runes[limit:]
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}

func cloneKeyboard(keyboard channel.Keyboard) channel.Keyboard {
	if keyboard == nil {
		return nil
	}
	copy := make(channel.Keyboard, len(keyboard))
	for i := range keyboard {
		copy[i] = append([]channel.Button(nil), keyboard[i]...)
	}
	return copy
}
