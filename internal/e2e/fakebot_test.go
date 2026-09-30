package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type fakeButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

type fakeMessage struct {
	ID         int64
	ChatID     int64
	Text       string
	Keyboard   [][]fakeButton
	ForceReply bool
}

type fakeBot struct {
	mu        sync.Mutex
	nextID    int64
	nextMsg   int64
	updates   []map[string]any
	messages  []fakeMessage
	edits     []map[string]any
	sent      chan fakeMessage
	editCh    chan map[string]any
	responder func(fakeMessage)
}

func newFakeBot() *fakeBot {
	return &fakeBot{sent: make(chan fakeMessage, 256), editCh: make(chan map[string]any, 256)}
}

func (f *fakeBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path
	if index := strings.LastIndex(method, "/"); index >= 0 {
		method = method[index+1:]
	}
	switch method {
	case "getMe":
		writeFakeJSON(w, map[string]any{"ok": true, "result": map[string]any{"id": 123, "is_bot": true, "first_name": "Bridge", "username": "agent_bridge_fake_bot"}})
	case "sendMessage":
		var request struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
			Markup struct {
				InlineKeyboard [][]fakeButton `json:"inline_keyboard"`
				ForceReply     bool           `json:"force_reply"`
			} `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.nextMsg++
		message := fakeMessage{ID: f.nextMsg, ChatID: request.ChatID, Text: request.Text, Keyboard: request.Markup.InlineKeyboard, ForceReply: request.Markup.ForceReply}
		f.messages = append(f.messages, message)
		responder := f.responder
		f.mu.Unlock()
		select {
		case f.sent <- message:
		default:
		}
		if responder != nil {
			responder(message)
		}
		writeFakeJSON(w, map[string]any{"ok": true, "result": map[string]any{"message_id": message.ID, "chat": map[string]any{"id": message.ChatID, "type": "private"}, "text": message.Text}})
	case "editMessageText":
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		f.mu.Lock()
		f.edits = append(f.edits, request)
		f.mu.Unlock()
		select {
		case f.editCh <- request:
		default:
		}
		writeFakeJSON(w, map[string]any{"ok": true, "result": true})
	case "editMessageReplyMarkup", "answerCallbackQuery":
		_, _ = io.Copy(io.Discard, r.Body)
		writeFakeJSON(w, map[string]any{"ok": true, "result": true})
	case "getUpdates":
		var request struct {
			Offset  int64 `json:"offset"`
			Timeout int   `json:"timeout"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		deadline := time.Now().Add(750 * time.Millisecond)
		for {
			f.mu.Lock()
			updates := make([]map[string]any, 0)
			for _, update := range f.updates {
				id := number(update["update_id"])
				if id >= request.Offset {
					updates = append(updates, update)
				}
			}
			f.mu.Unlock()
			if len(updates) > 0 {
				writeFakeJSON(w, map[string]any{"ok": true, "result": updates})
				return
			}
			if time.Now().After(deadline) {
				writeFakeJSON(w, map[string]any{"ok": true, "result": []any{}})
				return
			}
			select {
			case <-time.After(25 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	default:
		http.Error(w, "unknown Bot API method", http.StatusNotFound)
	}
}

func (f *fakeBot) SetResponder(responder func(fakeMessage)) {
	f.mu.Lock()
	f.responder = responder
	f.mu.Unlock()
}

func (f *fakeBot) PushMessage(chatID, replyTo int64, text string) {
	f.mu.Lock()
	f.nextID++
	updateID := f.nextID
	message := map[string]any{
		"message_id": f.nextMsg + 1000 + updateID,
		"text":       text,
		"chat":       map[string]any{"id": chatID, "type": "private"},
		"from":       map[string]any{"id": chatID, "first_name": "Bruno"},
	}
	if replyTo != 0 {
		message["reply_to_message"] = map[string]any{"message_id": replyTo, "chat": map[string]any{"id": chatID, "type": "private"}}
	}
	f.updates = append(f.updates, map[string]any{"update_id": updateID, "message": message})
	f.mu.Unlock()
}

func (f *fakeBot) PushCallback(chatID, messageID int64, callbackID, data string) {
	f.mu.Lock()
	f.nextID++
	f.updates = append(f.updates, map[string]any{"update_id": f.nextID, "callback_query": map[string]any{
		"id": callbackID, "data": data, "from": map[string]any{"id": chatID, "first_name": "Bruno"},
		"message": map[string]any{"message_id": messageID, "chat": map[string]any{"id": chatID, "type": "private"}},
	}})
	f.mu.Unlock()
}

func (f *fakeBot) WaitSend(deadline time.Time, match func(fakeMessage) bool) (fakeMessage, bool) {
	for time.Now().Before(deadline) {
		select {
		case message := <-f.sent:
			if match == nil || match(message) {
				return message, true
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	return fakeMessage{}, false
}

func (f *fakeBot) WaitEdit(deadline time.Time, match func(map[string]any) bool) (map[string]any, bool) {
	for time.Now().Before(deadline) {
		select {
		case edit := <-f.editCh:
			if match == nil || match(edit) {
				return edit, true
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil, false
}

func (f *fakeBot) Snapshot() ([]fakeMessage, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	messages := append([]fakeMessage(nil), f.messages...)
	edits := append([]map[string]any(nil), f.edits...)
	return messages, edits
}

func (f *fakeBot) UpdatesSnapshot() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	updates := append([]map[string]any(nil), f.updates...)
	return updates
}

func writeFakeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func number(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		result, _ := typed.Int64()
		return result
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		result, _ := strconv.ParseInt(typed, 10, 64)
		return result
	default:
		return 0
	}
}
