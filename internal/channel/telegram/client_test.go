package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brbbruno/agent-bridge/internal/channel"
)

func TestSplitTextLimit(t *testing.T) {
	text := strings.Repeat("a", 9000)
	chunks := SplitText(text, MaxMessageRunes)
	if len(chunks) != 3 {
		t.Fatalf("len(chunks)=%d", len(chunks))
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > MaxMessageRunes {
			t.Fatalf("chunk has %d runes", len([]rune(chunk)))
		}
	}
}

func TestSendSplitsAndSupportsForceReply(t *testing.T) {
	var got []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "botTOKEN/sendMessage") {
			t.Errorf("path=%s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		got = append(got, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": len(got), "chat": map[string]any{"id": 9, "type": "private"}}})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	message, err := client.Send(context.Background(), strings.Repeat("x", 5000), channel.Keyboard{{{Text: "Outro (texto)", Data: "q:a:0:o"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || message.ID != 2 || len(message.IDs) != 2 {
		t.Fatalf("messages=%d result=%+v", len(got), message)
	}
	if got[0]["reply_markup"] != nil {
		t.Fatal("markup deve ficar no último segmento")
	}
	markup, ok := got[1]["reply_markup"].(map[string]any)
	if !ok || markup["inline_keyboard"] == nil {
		t.Fatalf("inline keyboard ausente: %v", got[1]["reply_markup"])
	}
	_, err = client.Send(context.Background(), "Digite sua resposta", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	force, ok := got[2]["reply_markup"].(map[string]any)
	if !ok || force["force_reply"] != true {
		t.Fatalf("ForceReply ausente: %v", got[2]["reply_markup"])
	}
	if got[2]["chat_id"] != float64(9) {
		t.Fatalf("chat_id=%v", got[2]["chat_id"])
	}
}

func TestUpdatesAllowlistAndCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{
			map[string]any{"update_id": 1, "message": map[string]any{"message_id": 11, "text": "ignored", "chat": map[string]any{"id": 8, "type": "private"}, "from": map[string]any{"id": 8}}},
			map[string]any{"update_id": 2, "callback_query": map[string]any{"id": "cb1", "data": "p:x:a", "from": map[string]any{"id": 9}, "message": map[string]any{"message_id": 12, "chat": map[string]any{"id": 9, "type": "private"}}}},
		}})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	updates, err := client.Updates(context.Background(), 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || !updates[0].Ignored || updates[1].CallbackID != "cb1" || updates[1].CallbackData != "p:x:a" || updates[1].ChatID != 9 {
		t.Fatalf("updates=%+v", updates)
	}
}

func TestAnswerCallbackQuery(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "answerCallbackQuery") {
			t.Errorf("path=%s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		called = body["callback_query_id"] == "callback-123"
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	if err := client.AnswerCallback(context.Background(), "callback-123", "Resposta recebida"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("callback não reconhecido pela API fake")
	}
}

func TestSendReplyAndEditReplyMarkup(t *testing.T) {
	methods := make(chan string, 2)
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		methods <- method
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		if method == "sendMessage" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 42, "chat": map[string]any{"id": 9, "type": "private"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	if _, err := client.SendReply(context.Background(), 41, "Status da solicitação"); err != nil {
		t.Fatal(err)
	}
	if err := client.EditReplyMarkup(context.Background(), 41, nil); err != nil {
		t.Fatal(err)
	}
	if method := <-methods; method != "sendMessage" {
		t.Fatalf("método de resposta=%q", method)
	}
	reply := <-requests
	if reply["reply_to_message_id"] != float64(41) {
		t.Fatalf("resposta não vinculada: %v", reply)
	}
	if method := <-methods; method != "editMessageReplyMarkup" {
		t.Fatalf("método de markup=%q", method)
	}
	markupRequest := <-requests
	markup, ok := markupRequest["reply_markup"].(map[string]any)
	if !ok || markup["inline_keyboard"] == nil {
		t.Fatalf("markup não foi removido: %v", markupRequest["reply_markup"])
	}
}

func TestCallbackDataAndReplyMarkupValidation(t *testing.T) {
	if _, err := makeMarkup(channel.Keyboard{{{Text: "x", Data: strings.Repeat("a", 65)}}}, false); err == nil {
		t.Fatal("callback_data >64 bytes accepted")
	}
	if _, err := makeMarkup(channel.Keyboard{{{Text: "x", Data: "ok"}}}, true); err == nil {
		t.Fatal("inline keyboard e ForceReply foram combinados")
	}
}

func TestTelegramErrorsNeverExposeToken(t *testing.T) {
	const token = "secret-e2e-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "token " + token + " inválido"})
	}))
	defer server.Close()
	client := New(token, 9, server.URL)
	client.SetHTTPClient(server.Client())
	_, err := client.GetMe(context.Background())
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("erro ausente ou expôs token: %v", err)
	}
}
