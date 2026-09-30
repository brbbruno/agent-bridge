package channel

import "context"

const MaxMessageRunes = 4096

type Button struct {
	Text string `json:"text"`
	Data string `json:"data"`
}

type Keyboard [][]Button

type SentMessage struct {
	ID     int64
	IDs    []int64
	Chunks []string
}

type Update struct {
	ID             int64
	ChatID         int64
	UserID         int64
	FirstName      string
	Username       string
	Private        bool
	MessageID      int64
	ReplyToMessage int64
	Text           string
	CallbackID     string
	CallbackData   string
	Ignored        bool
}

type Channel interface {
	Send(ctx context.Context, text string, keyboard Keyboard, forceReply bool) (SentMessage, error)
	SendReply(ctx context.Context, replyToMessageID int64, text string) (SentMessage, error)
	Edit(ctx context.Context, messageID int64, text string, keyboard Keyboard) error
	EditReplyMarkup(ctx context.Context, messageID int64, keyboard Keyboard) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
	Updates(ctx context.Context, offset int64, timeoutSeconds int) ([]Update, error)
}
