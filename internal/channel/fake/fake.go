package fake

import (
	"context"
	"sync"

	"github.com/brbbruno/agent-bridge/internal/channel"
)

type SentRecord struct {
	ID         int64
	Text       string
	Keyboard   channel.Keyboard
	ForceReply bool
	ReplyTo    int64
}

type EditRecord struct {
	ID         int64
	Text       string
	Keyboard   channel.Keyboard
	MarkupOnly bool
}

type Fake struct {
	mu        sync.Mutex
	nextID    int64
	Sent      []SentRecord
	Edits     []EditRecord
	Callbacks []string
	UpdatesCh chan channel.Update
}

func New() *Fake { return &Fake{UpdatesCh: make(chan channel.Update, 32)} }

func (f *Fake) Send(_ context.Context, text string, keyboard channel.Keyboard, forceReply bool) (channel.SentMessage, error) {
	return f.send(text, keyboard, forceReply, 0)
}

func (f *Fake) SendReply(_ context.Context, replyToMessageID int64, text string) (channel.SentMessage, error) {
	return f.send(text, nil, false, replyToMessageID)
}

func (f *Fake) send(text string, keyboard channel.Keyboard, forceReply bool, replyTo int64) (channel.SentMessage, error) {
	chunks := splitText(text)
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0, len(chunks))
	for index, chunk := range chunks {
		f.nextID++
		ids = append(ids, f.nextID)
		markup := channel.Keyboard(nil)
		force := false
		if index == len(chunks)-1 {
			markup = keyboard
			force = forceReply
		}
		f.Sent = append(f.Sent, SentRecord{ID: f.nextID, Text: chunk, Keyboard: cloneKeyboard(markup), ForceReply: force, ReplyTo: replyTo})
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

func (f *Fake) AnswerCallback(_ context.Context, callbackID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Callbacks = append(f.Callbacks, callbackID)
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

func splitText(text string) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	chunks := make([]string, 0, len(runes)/channel.MaxMessageRunes+1)
	for len(runes) > channel.MaxMessageRunes {
		chunks = append(chunks, string(runes[:channel.MaxMessageRunes]))
		runes = runes[channel.MaxMessageRunes:]
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
