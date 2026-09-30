package daemon

import (
	"sort"
	"time"
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

func (r *Router) addLateMessagesLocked(messageIDs []int64, session string, now time.Time) {
	for _, messageID := range messageIDs {
		if messageID != 0 {
			r.lateMessage[messageID] = lateMessageEntry{sessionID: session, createdAt: now}
		}
	}
	r.pruneLateMessagesLocked(now)
}

func (r *Router) pruneLateMessagesLocked(now time.Time) {
	for messageID, entry := range r.lateMessage {
		if now.Sub(entry.createdAt) > lateMessageLifetime {
			delete(r.lateMessage, messageID)
		}
	}
	if len(r.lateMessage) > lateMessageLimit {
		type record struct {
			messageID int64
			createdAt time.Time
		}
		records := make([]record, 0, len(r.lateMessage))
		for messageID, entry := range r.lateMessage {
			records = append(records, record{messageID: messageID, createdAt: entry.createdAt})
		}
		sort.Slice(records, func(i, j int) bool {
			if records[i].createdAt.Equal(records[j].createdAt) {
				return records[i].messageID < records[j].messageID
			}
			return records[i].createdAt.Before(records[j].createdAt)
		})
		for _, record := range records[:len(records)-lateMessageLimit] {
			delete(r.lateMessage, record.messageID)
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
