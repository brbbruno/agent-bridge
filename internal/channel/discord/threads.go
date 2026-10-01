package discord

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/bwmarrin/discordgo"
)

const threadLifetime = 30 * 24 * time.Hour

type threadRecord struct {
	ThreadID  string    `json:"thread_id"`
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

func loadThreads(home string) (map[string]threadRecord, error) {
	threads := map[string]threadRecord{}
	data, err := os.ReadFile(filepath.Join(home, "discord-threads.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return threads, nil
		}
		return threads, err
	}
	if err := json.Unmarshal(data, &threads); err != nil {
		return map[string]threadRecord{}, err
	}
	if threads == nil {
		threads = map[string]threadRecord{}
	}
	cutoff := time.Now().Add(-threadLifetime)
	pruned := false
	for session, record := range threads {
		if record.UpdatedAt.Before(cutoff) {
			delete(threads, session)
			pruned = true
		}
	}
	if pruned {
		return threads, saveThreadFile(home, threads)
	}
	return threads, nil
}

func (c *Channel) destination(ctx context.Context, client api, session channel.SessionRef) (string, error) {
	c.threadMu.Lock()
	defer c.threadMu.Unlock()
	c.mu.Lock()
	bound := c.cfg.Discord.ChannelID
	c.mu.Unlock()
	if bound == "" {
		return "", c.unboundError()
	}
	if session.ID == "" {
		return bound, nil
	}
	name := threadName(session)
	c.mu.Lock()
	record, exists := c.threads[session.ID]
	lastRename := c.lastRename[session.ID]
	c.mu.Unlock()
	now := time.Now()
	if !exists {
		return c.startThread(ctx, client, bound, session, name, now)
	}
	if record.Name != name && now.Sub(lastRename) >= 10*time.Minute {
		c.mu.Lock()
		c.lastRename[session.ID] = now
		c.mu.Unlock()
		if _, err := client.ChannelEdit(record.ThreadID, &discordgo.ChannelEdit{Name: name}); err != nil {
			c.logError("renomear thread Discord: %v", err)
		} else {
			record.Name = name
		}
	}
	record.UpdatedAt = now
	c.mu.Lock()
	c.threads[session.ID] = record
	c.threadIDs[record.ThreadID] = session.ID
	c.channelCache[record.ThreadID] = channelMeta{parentID: bound, kind: discordgo.ChannelTypeGuildPublicThread}
	if err := c.saveThreadsLocked(); err != nil {
		c.logErrorLocked("salvar threads Discord: %v", err)
	}
	c.mu.Unlock()
	return record.ThreadID, nil
}

func (c *Channel) recreateThread(ctx context.Context, client api, session channel.SessionRef) (string, error) {
	c.threadMu.Lock()
	defer c.threadMu.Unlock()
	c.mu.Lock()
	bound := c.cfg.Discord.ChannelID
	c.mu.Unlock()
	if bound == "" {
		return "", c.unboundError()
	}
	return c.startThread(ctx, client, bound, session, threadName(session), time.Now())
}

func (c *Channel) startThread(ctx context.Context, client api, parentID string, session channel.SessionRef, name string, now time.Time) (string, error) {
	thread, err := client.ThreadStartComplex(parentID, &discordgo.ThreadStart{
		Name:                name,
		Type:                discordgo.ChannelTypeGuildPublicThread,
		AutoArchiveDuration: 10080,
	})
	if err != nil {
		return "", err
	}
	if thread == nil || thread.ID == "" {
		return "", errors.New("Discord não retornou o ID da thread criada")
	}
	record := threadRecord{ThreadID: thread.ID, Name: name, UpdatedAt: now}
	c.mu.Lock()
	if previous, exists := c.threads[session.ID]; exists {
		delete(c.threadIDs, previous.ThreadID)
		delete(c.channelCache, previous.ThreadID)
	}
	c.threads[session.ID] = record
	c.threadIDs[thread.ID] = session.ID
	c.lastRename[session.ID] = now
	c.channelCache[thread.ID] = channelMeta{parentID: parentID, kind: discordgo.ChannelTypeGuildPublicThread}
	if err := c.saveThreadsLocked(); err != nil {
		c.logErrorLocked("salvar threads Discord: %v", err)
	}
	c.mu.Unlock()
	return thread.ID, nil
}

func (c *Channel) saveThreadsLocked() error {
	return saveThreadFile(c.home, c.threads)
}

func saveThreadFile(home string, threads map[string]threadRecord) error {
	data, err := json.MarshalIndent(threads, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(home, ".discord-threads-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(home, "discord-threads.json"))
}

func threadName(session channel.SessionRef) string {
	name := strings.TrimSpace(session.Name)
	if name == "" {
		name = session.ID
	}
	title := strings.TrimSpace(session.Title)
	if title != "" {
		name = title + " (" + name + ")"
	}
	if utf8.RuneCountInString(name) > 100 {
		name = string([]rune(name)[:100])
	}
	return name
}

func (c *Channel) unboundError() error {
	return errors.New("canal Discord não vinculado; use /vincular computador:" + c.machine)
}

func isUnknownChannel(err error) bool {
	var restErr *discordgo.RESTError
	return errors.As(err, &restErr) && restErr.Message != nil && restErr.Message.Code == 10003
}
