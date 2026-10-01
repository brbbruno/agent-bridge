package channel

import "context"

const MaxMessageRunes = 4096

type Button struct {
	Text string `json:"text"`
	Data string `json:"data"`
}

type Keyboard [][]Button

type SessionRef struct {
	ID      string
	Name    string
	Title   string
	Project string
	Agent   string
}

type Outgoing struct {
	Session  SessionRef
	Text     string
	Keyboard Keyboard
	ReplyTo  int64
}

type MessageKey struct {
	Channel string
	ID      int64
}

type SentMessage struct {
	ID     int64
	IDs    []int64
	Chunks []string
}

type Update struct {
	Channel        string
	ID             int64
	ChatID         int64
	UserID         int64
	FirstName      string
	Username       string
	Private        bool
	MessageID      int64
	ReplyToMessage int64
	Text           string
	TextToken      string
	SessionID      string
	CallbackID     string
	CallbackData   string
	Ignored        bool
}

type Channel interface {
	Name() string
	MessageLimit() int
	Run(ctx context.Context, handle func(context.Context, Update)) error
	Send(ctx context.Context, message Outgoing) (SentMessage, error)
	Edit(ctx context.Context, messageID int64, text string, keyboard Keyboard) error
	EditReplyMarkup(ctx context.Context, messageID int64, keyboard Keyboard) error
	AnswerCallback(ctx context.Context, update Update, text string) error
	RequestText(ctx context.Context, update Update, session SessionRef, prompt, token string) (SentMessage, error)
}
