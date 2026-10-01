package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
)

func TestSendSplitsAndSupportsForceReply(t *testing.T) {
	var got []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/answerCallbackQuery") {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
			return
		}
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
	message, err := client.Send(context.Background(), channel.Outgoing{Text: strings.Repeat("x", 5000), Keyboard: channel.Keyboard{{{Text: "Outro (texto)", Data: "q:a:0:o", Style: "success"}}}})
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
	rows := markup["inline_keyboard"].([]any)
	button := rows[0].([]any)[0].(map[string]any)
	if _, hasStyle := button["style"]; hasStyle {
		t.Fatalf("estilo Discord vazou para o Telegram: %+v", button)
	}
	_, err = client.RequestText(context.Background(), channel.Update{CallbackID: "request-text"}, channel.SessionRef{}, "Digite sua resposta", "token")
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
	if err := client.AnswerCallback(context.Background(), channel.Update{CallbackID: "callback-123"}, "Resposta recebida"); err != nil {
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
	if _, err := client.Send(context.Background(), channel.Outgoing{ReplyTo: 41, Text: "Status da solicitação"}); err != nil {
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

func TestSendRendersHTMLAndKeepsSourceChunks(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 7}})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	source := "**bold** & <tag>"
	message, err := client.Send(context.Background(), channel.Outgoing{Text: source})
	if err != nil {
		t.Fatal(err)
	}
	if request["text"] != "<b>bold</b> &amp; &lt;tag&gt;" || request["parse_mode"] != "HTML" {
		t.Fatalf("request não renderizado em HTML: %+v", request)
	}
	if len(message.Chunks) != 1 || message.Chunks[0] != source {
		t.Fatalf("chunks não preservaram o Markdown de origem: %+v", message.Chunks)
	}
}

func TestSendRetriesWithoutHTMLWhenTelegramCannotParseEntities(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		if len(requests) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Bad Request: can't parse entities: malformed tag"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 12}})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	source := "**bold**"
	message, err := client.Send(context.Background(), channel.Outgoing{Text: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0]["text"] != "<b>bold</b>" || requests[0]["parse_mode"] != "HTML" {
		t.Fatalf("primeira tentativa inesperada: %+v", requests)
	}
	if requests[1]["text"] != source || requests[1]["parse_mode"] != nil {
		t.Fatalf("fallback não enviou Markdown simples: %+v", requests[1])
	}
	if len(message.Chunks) != 1 || message.Chunks[0] != source {
		t.Fatalf("chunks não preservaram a origem no fallback: %+v", message.Chunks)
	}
}

func TestEditRetriesWithoutHTMLWhenTelegramCannotParseEntities(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		if len(requests) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Bad Request: can't parse entities: malformed tag"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	if err := client.Edit(context.Background(), 42, "**bold**", nil); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0]["parse_mode"] != "HTML" || requests[1]["parse_mode"] != nil || requests[1]["text"] != "**bold**" {
		t.Fatalf("tentativas de edição inesperadas: %+v", requests)
	}
}

func TestEditTreatsUnchangedMessagesAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Bad Request: message is not modified"})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	if err := client.Edit(context.Background(), 42, "same", nil); err != nil {
		t.Fatalf("Edit retornou erro para mensagem sem alteração: %v", err)
	}
	if err := client.EditReplyMarkup(context.Background(), 42, nil); err != nil {
		t.Fatalf("EditReplyMarkup retornou erro para markup sem alteração: %v", err)
	}
}

func TestTelegramRunSetsChannelAndAdvancesOffset(t *testing.T) {
	secondOffset := make(chan int64, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Offset int64 `json:"offset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Offset == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{
				map[string]any{"update_id": 1, "message": map[string]any{"message_id": 12, "text": "oi", "chat": map[string]any{"id": 9, "type": "private"}, "from": map[string]any{"id": 8}}},
			}})
			return
		}
		secondOffset <- request.Offset
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan channel.Update, 1)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, func(_ context.Context, update channel.Update) { updates <- update }) }()
	select {
	case update := <-updates:
		if update.Channel != "Telegram" || update.Text != "oi" {
			t.Fatalf("update=%+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não entregou a atualização")
	}
	select {
	case offset := <-secondOffset:
		if offset != 2 {
			t.Fatalf("offset seguinte=%d; esperado 2", offset)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não avançou o offset")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não encerrou após cancelar o contexto")
	}
}

func TestTelegramRequestTextUsesForceReply(t *testing.T) {
	var request map[string]any
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		methods = append(methods, method)
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if method == "answerCallbackQuery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 31}})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	message, err := client.RequestText(context.Background(), channel.Update{MessageID: 24, CallbackID: "cb-text"}, channel.SessionRef{ID: "s"}, "Digite sua resposta", "t:p")
	if err != nil {
		t.Fatal(err)
	}
	markup, ok := request["reply_markup"].(map[string]any)
	if !ok || markup["force_reply"] != true || message.ID != 31 {
		t.Fatalf("RequestText request=%+v message=%+v", request, message)
	}
	if len(methods) != 2 || methods[0] != "answerCallbackQuery" || methods[1] != "sendMessage" {
		t.Fatalf("ordem de RequestText=%v", methods)
	}
}

func TestTelegramRunBackoffGrowsAndCaps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "offline"})
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	var delays []time.Duration
	client.sleep = func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return len(delays) < 7
	}
	if err := client.Run(context.Background(), func(context.Context, channel.Update) {}); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("backoff delays=%v", delays)
	}
	for index := range want {
		if delays[index] != want[index] {
			t.Fatalf("backoff delays=%v, want %v", delays, want)
		}
	}
}

func TestTelegramRunSkipsOldBacklogAndSendsNotice(t *testing.T) {
	start := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	notices := make(chan string, 2)
	secondOffset := make(chan int64, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			text, _ := request["text"].(string)
			notices <- text
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 40}})
			return
		}
		var request struct {
			Offset int64 `json:"offset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Offset == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{
				map[string]any{"update_id": 1, "message": map[string]any{"message_id": 11, "date": start.Add(-11 * time.Minute).Unix(), "text": "antiga", "chat": map[string]any{"id": 9, "type": "private"}, "from": map[string]any{"id": 9}}},
				map[string]any{"update_id": 2, "message": map[string]any{"message_id": 12, "date": start.Add(-5 * time.Minute).Unix(), "text": "recente", "chat": map[string]any{"id": 9, "type": "private"}, "from": map[string]any{"id": 9}}},
			}})
			return
		}
		secondOffset <- request.Offset
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	client.now = func() time.Time { return start }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan channel.Update, 2)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, func(_ context.Context, update channel.Update) { updates <- update }) }()
	select {
	case update := <-updates:
		if update.Text != "recente" || !update.Time.Equal(start.Add(-5*time.Minute)) {
			t.Fatalf("update entregue inesperado: %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não entregou a mensagem recente")
	}
	select {
	case notice := <-notices:
		if notice != "1 mensagem enviada enquanto o agent-bridge estava parado foi ignorada. Reenvie se ainda for necessário." {
			t.Fatalf("aviso inesperado: %q", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não enviou aviso sobre a mensagem antiga")
	}
	select {
	case offset := <-secondOffset:
		if offset != 3 {
			t.Fatalf("offset seguinte=%d; esperado 3", offset)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não avançou o offset além das mensagens antigas e recentes")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não encerrou após cancelar o contexto")
	}
	if len(updates) != 0 || len(notices) != 0 {
		t.Fatalf("atualizações extras=%d, avisos extras=%d", len(updates), len(notices))
	}
}

func TestTelegramRunCallbackHasUnknownTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Offset int64 `json:"offset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Offset == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{
				map[string]any{"update_id": 7, "callback_query": map[string]any{"id": "cb", "data": "ok", "from": map[string]any{"id": 9}, "message": map[string]any{"message_id": 12, "date": time.Now().Unix(), "chat": map[string]any{"id": 9, "type": "private"}}}},
			}})
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New("TOKEN", 9, server.URL)
	client.SetHTTPClient(server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan channel.Update, 1)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, func(_ context.Context, update channel.Update) { updates <- update }) }()
	select {
	case update := <-updates:
		if update.CallbackID != "cb" || !update.Time.IsZero() {
			t.Fatalf("callback=%+v; esperado Time zero", update)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não entregou o callback")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não encerrou após cancelar o contexto")
	}
}
