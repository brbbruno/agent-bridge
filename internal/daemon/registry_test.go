package daemon

import (
	"testing"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
)

func TestPruneLateMessagesByAgeAndOldestFirstLimit(t *testing.T) {
	router, _, _ := newTestRouter(t, time.Second)
	now := time.Now()
	router.mu.Lock()
	router.sessionLabels["late-label"] = "late-session"
	router.sessionLabels["waiting-label"] = "waiting-session"
	router.sessionLabels["queued-label"] = "queued-session"
	router.sessionLabels["stale-label"] = "stale-session"
	router.waiting["waiting-session"] = "pending-id"
	router.state.Queues["queued-session"] = []string{"resposta pendente"}
	for id := int64(1); id <= lateMessageLimit+3; id++ {
		key := channel.MessageKey{Channel: "Telegram", ID: id}
		router.lateMessage[key] = lateMessageEntry{sessionID: "late-session", createdAt: now.Add(time.Duration(id) * time.Second)}
	}
	router.pruneLateMessagesLocked(now)
	if len(router.lateMessage) != lateMessageLimit {
		router.mu.Unlock()
		t.Fatalf("lateMessage=%d, esperava limite %d", len(router.lateMessage), lateMessageLimit)
	}
	for id := int64(1); id <= 3; id++ {
		if _, exists := router.lateMessage[channel.MessageKey{Channel: "Telegram", ID: id}]; exists {
			router.mu.Unlock()
			t.Fatalf("mensagem mais antiga %d permaneceu na fila", id)
		}
	}
	if _, exists := router.sessionLabels["stale-label"]; exists {
		router.mu.Unlock()
		t.Fatal("label sem sessão ativa não foi removido")
	}
	for id, entry := range router.lateMessage {
		entry.createdAt = now.Add(-25 * time.Hour)
		router.lateMessage[id] = entry
	}
	router.pruneLateMessagesLocked(now)
	if len(router.lateMessage) != 0 {
		router.mu.Unlock()
		t.Fatalf("entradas antigas não expiraram: %d", len(router.lateMessage))
	}
	if _, exists := router.sessionLabels["late-label"]; exists {
		router.mu.Unlock()
		t.Fatal("label sem pending, fila ou lateMessage não foi removido")
	}
	if _, exists := router.sessionLabels["waiting-label"]; !exists {
		router.mu.Unlock()
		t.Fatal("label de sessão aguardando foi removido")
	}
	if _, exists := router.sessionLabels["queued-label"]; !exists {
		router.mu.Unlock()
		t.Fatal("label com resposta enfileirada foi removido")
	}
	delete(router.waiting, "waiting-session")
	delete(router.state.Queues, "queued-session")
	router.pruneSessionLabelsLocked()
	if len(router.sessionLabels) != 0 {
		router.mu.Unlock()
		t.Fatalf("labels inativos permaneceram: %v", router.sessionLabels)
	}
	router.mu.Unlock()
}

func TestPruneEndedSessionsBoundsTombstones(t *testing.T) {
	router, _, _ := newTestRouter(t, time.Second)
	now := time.Now()
	router.mu.Lock()
	for index := 1; index <= endedSessionLimit+2; index++ {
		router.endedSessions[string(rune('a'+index))] = now.Add(time.Duration(index) * time.Second)
	}
	router.pruneEndedSessionsLocked(now)
	if len(router.endedSessions) != endedSessionLimit {
		router.mu.Unlock()
		t.Fatalf("endedSessions=%d, esperava limite %d", len(router.endedSessions), endedSessionLimit)
	}
	router.mu.Unlock()
}
