package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brbbruno/agent-bridge/internal/channel"
)

const MaxMessageRunes = channel.MaxMessageRunes

type Client struct {
	token  string
	chatID int64
	base   string
	http   *http.Client
}

func New(token string, chatID int64, apiBase string) *Client {
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	return &Client{token: token, chatID: chatID, base: strings.TrimRight(apiBase, "/"), http: &http.Client{Timeout: 40 * time.Second}}
}

func (c *Client) SetHTTPClient(client *http.Client) {
	if client != nil {
		c.http = client
	}
}

type APIError struct {
	Method      string
	Description string
}

func (e *APIError) Error() string { return fmt.Sprintf("Telegram %s: %s", e.Method, e.Description) }

type apiEnvelope[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
}

func (c *Client) call(ctx context.Context, method string, input any, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	endpoint := c.base + "/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return scrub(err, c.token)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return scrub(err, c.token)
	}
	defer resp.Body.Close()
	var envelope apiEnvelope[json.RawMessage]
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("Telegram %s: resposta inválida: %w", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.OK {
		description := strings.ReplaceAll(envelope.Description, c.token, "[token redigido]")
		return &APIError{Method: method, Description: description}
	}
	if output == nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, output); err != nil {
		return fmt.Errorf("Telegram %s: resultado inválido: %w", method, err)
	}
	return nil
}

func scrub(err error, token string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	if token != "" {
		text = strings.ReplaceAll(text, token, "[token redigido]")
	}
	return errors.New(text)
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	First    string `json:"first_name"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type APIMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      Chat   `json:"chat"`
	From      User   `json:"from"`
	ReplyTo   *struct {
		MessageID int64 `json:"message_id"`
	} `json:"reply_to_message"`
}

type CallbackQuery struct {
	ID      string      `json:"id"`
	From    User        `json:"from"`
	Data    string      `json:"data"`
	Message *APIMessage `json:"message"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *APIMessage    `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Me struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

func (c *Client) GetMe(ctx context.Context) (Me, error) {
	var me Me
	err := c.call(ctx, "getMe", map[string]any{}, &me)
	return me, err
}

type keyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type replyMarkup struct {
	InlineKeyboard [][]keyboardButton `json:"inline_keyboard,omitempty"`
	ForceReply     bool               `json:"force_reply,omitempty"`
	Selective      bool               `json:"selective,omitempty"`
}

func makeMarkup(keyboard channel.Keyboard, forceReply bool) (replyMarkup, error) {
	markup := replyMarkup{}
	if forceReply && len(keyboard) > 0 {
		return replyMarkup{}, errors.New("ForceReply e InlineKeyboardMarkup não podem ser combinados")
	}
	if len(keyboard) > 0 {
		markup.InlineKeyboard = make([][]keyboardButton, len(keyboard))
		for rowIndex, row := range keyboard {
			markup.InlineKeyboard[rowIndex] = make([]keyboardButton, len(row))
			for colIndex, button := range row {
				if len(button.Data) > 64 {
					return replyMarkup{}, fmt.Errorf("callback_data excede 64 bytes (%d)", len(button.Data))
				}
				markup.InlineKeyboard[rowIndex][colIndex] = keyboardButton{Text: button.Text, CallbackData: button.Data}
			}
		}
	}
	if forceReply {
		markup.ForceReply = true
		markup.Selective = true
	}
	return markup, nil
}

func SplitText(text string, limit int) []string {
	if limit <= 0 {
		limit = MaxMessageRunes
	}
	if text == "" {
		return []string{""}
	}
	chunks := make([]string, 0, utf8.RuneCountInString(text)/limit+1)
	remaining := []rune(text)
	for len(remaining) > limit {
		cut := limit
		for i := limit - 1; i > limit*3/4; i-- {
			if remaining[i] == '\n' || remaining[i] == ' ' {
				cut = i + 1
				break
			}
		}
		chunks = append(chunks, string(remaining[:cut]))
		remaining = remaining[cut:]
	}
	if len(remaining) > 0 {
		chunks = append(chunks, string(remaining))
	}
	return chunks
}

func (c *Client) Send(ctx context.Context, text string, keyboard channel.Keyboard, forceReply bool) (channel.SentMessage, error) {
	return c.send(ctx, text, keyboard, forceReply, 0)
}

func (c *Client) SendReply(ctx context.Context, replyToMessageID int64, text string) (channel.SentMessage, error) {
	return c.send(ctx, text, nil, false, replyToMessageID)
}

func (c *Client) send(ctx context.Context, text string, keyboard channel.Keyboard, forceReply bool, replyToMessageID int64) (channel.SentMessage, error) {
	chunks := SplitText(text, MaxMessageRunes)
	ids := make([]int64, 0, len(chunks))
	for i, chunk := range chunks {
		markup := channel.Keyboard(nil)
		force := false
		if i == len(chunks)-1 {
			markup = keyboard
			force = forceReply
		}
		encoded, err := makeMarkup(markup, force)
		if err != nil {
			return channel.SentMessage{}, err
		}
		var sent APIMessage
		request := map[string]any{"chat_id": c.chatID, "text": chunk}
		if replyToMessageID != 0 {
			request["reply_to_message_id"] = replyToMessageID
		}
		if len(markup) > 0 || force {
			request["reply_markup"] = encoded
		}
		if err := c.call(ctx, "sendMessage", request, &sent); err != nil {
			return channel.SentMessage{}, err
		}
		ids = append(ids, sent.MessageID)
	}
	result := channel.SentMessage{IDs: ids, Chunks: append([]string(nil), chunks...)}
	if len(ids) > 0 {
		result.ID = ids[len(ids)-1]
	}
	return result, nil
}

func (c *Client) Edit(ctx context.Context, messageID int64, text string, keyboard channel.Keyboard) error {
	if utf8.RuneCountInString(text) > MaxMessageRunes {
		return errors.New("texto de edição excede o limite do Telegram")
	}
	markup, err := makeMarkup(keyboard, false)
	if err != nil {
		return err
	}
	request := map[string]any{"chat_id": c.chatID, "message_id": messageID, "text": text}
	if keyboard == nil {
		request["reply_markup"] = map[string]any{"inline_keyboard": []any{}}
	} else {
		request["reply_markup"] = markup
	}
	return c.call(ctx, "editMessageText", request, nil)
}

func (c *Client) EditReplyMarkup(ctx context.Context, messageID int64, keyboard channel.Keyboard) error {
	request := map[string]any{"chat_id": c.chatID, "message_id": messageID}
	if len(keyboard) == 0 {
		request["reply_markup"] = map[string]any{"inline_keyboard": []any{}}
	} else {
		markup, err := makeMarkup(keyboard, false)
		if err != nil {
			return err
		}
		request["reply_markup"] = markup
	}
	return c.call(ctx, "editMessageReplyMarkup", request, nil)
}

func (c *Client) AnswerCallback(ctx context.Context, callbackID, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text}, nil)
}

func (c *Client) Updates(ctx context.Context, offset int64, timeoutSeconds int) ([]channel.Update, error) {
	if timeoutSeconds < 0 {
		timeoutSeconds = 0
	}
	if timeoutSeconds > 30 {
		timeoutSeconds = 30
	}
	var updates []Update
	if err := c.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": timeoutSeconds, "allowed_updates": []string{"message", "callback_query"}}, &updates); err != nil {
		return nil, err
	}
	result := make([]channel.Update, 0, len(updates))
	for _, update := range updates {
		if update.Message != nil {
			message := update.Message
			if c.chatID != 0 && message.Chat.ID != c.chatID {
				result = append(result, channel.Update{ID: update.UpdateID, Ignored: true})
				continue
			}
			item := channel.Update{ID: update.UpdateID, ChatID: message.Chat.ID, UserID: message.From.ID, FirstName: message.From.First, Username: message.From.Username, Private: message.Chat.Type == "private", MessageID: message.MessageID, Text: message.Text}
			if message.ReplyTo != nil {
				item.ReplyToMessage = message.ReplyTo.MessageID
			}
			result = append(result, item)
			continue
		}
		if update.CallbackQuery != nil && update.CallbackQuery.Message != nil {
			callback := update.CallbackQuery
			message := callback.Message
			if c.chatID != 0 && message.Chat.ID != c.chatID {
				result = append(result, channel.Update{ID: update.UpdateID, Ignored: true})
				continue
			}
			result = append(result, channel.Update{ID: update.UpdateID, ChatID: message.Chat.ID, UserID: callback.From.ID, FirstName: callback.From.First, Username: callback.From.Username, Private: message.Chat.Type == "private", MessageID: message.MessageID, CallbackID: callback.ID, CallbackData: callback.Data})
			continue
		}
		result = append(result, channel.Update{ID: update.UpdateID, Ignored: true})
	}
	return result, nil
}
