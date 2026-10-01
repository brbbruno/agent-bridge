package daemon

import (
	"sort"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
)

const (
	lateMessageLifetime = 24 * time.Hour
	lateMessageLimit    = 1000
	endedSessionLimit   = 1000
)

type lateMessageEntry struct {
	sessionID string
	createdAt time.Time
}

func (r *Router) addLateMessagesLocked(messageKeys []channel.MessageKey, session string, now time.Time) {
	for _, key := range messageKeys {
		if key.ID != 0 {
			r.lateMessage[key] = lateMessageEntry{sessionID: session, createdAt: now}
		}
	}
	r.pruneLateMessagesLocked(now)
}

func (r *Router) pruneLateMessagesLocked(now time.Time) {
	for key, entry := range r.lateMessage {
		if now.Sub(entry.createdAt) > lateMessageLifetime {
			delete(r.lateMessage, key)
		}
	}
	if len(r.lateMessage) > lateMessageLimit {
		type record struct {
			key       channel.MessageKey
			createdAt time.Time
		}
		records := make([]record, 0, len(r.lateMessage))
		for key, entry := range r.lateMessage {
			records = append(records, record{key: key, createdAt: entry.createdAt})
		}
		sort.Slice(records, func(i, j int) bool {
			if records[i].createdAt.Equal(records[j].createdAt) {
				if records[i].key.Channel == records[j].key.Channel {
					return records[i].key.ID < records[j].key.ID
				}
				return records[i].key.Channel < records[j].key.Channel
			}
			return records[i].createdAt.Before(records[j].createdAt)
		})
		for _, record := range records[:len(records)-lateMessageLimit] {
			delete(r.lateMessage, record.key)
		}
	}
	r.pruneSessionLabelsLocked()
}

func (r *Router) pruneSessionLabelsLocked() {
	active := make(map[string]struct{}, len(r.waiting)+len(r.state.Queues)+len(r.lateMessage))
	for session := range r.waiting {
		active[session] = struct{}{}
	}
	for session, replies := range r.state.Queues {
		if len(replies) > 0 {
			active[session] = struct{}{}
		}
	}
	for _, entry := range r.lateMessage {
		active[entry.sessionID] = struct{}{}
	}
	for label, session := range r.sessionLabels {
		if _, ok := active[session]; !ok {
			delete(r.sessionLabels, label)
		}
	}
}

func (r *Router) pruneEndedSessionsLocked(now time.Time) {
	for session, endedAt := range r.endedSessions {
		if now.Sub(endedAt) > lateMessageLifetime {
			delete(r.endedSessions, session)
		}
	}
	if len(r.endedSessions) <= endedSessionLimit {
		return
	}
	type record struct {
		session string
		endedAt time.Time
	}
	records := make([]record, 0, len(r.endedSessions))
	for session, endedAt := range r.endedSessions {
		records = append(records, record{session: session, endedAt: endedAt})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].endedAt.Equal(records[j].endedAt) {
			return records[i].session < records[j].session
		}
		return records[i].endedAt.Before(records[j].endedAt)
	})
	for _, record := range records[:len(records)-endedSessionLimit] {
		delete(r.endedSessions, record.session)
	}
}
