package discord

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/logx"
	"github.com/bwmarrin/discordgo"
)

const messageLimit = 2000

type interactionState struct {
	interaction *discordgo.Interaction
	createdAt   time.Time
	responded   bool
	command     bool
	deferred    bool
	replied     bool
}

type messageRoute struct {
	channelID string
	createdAt time.Time
}

type channelMeta struct {
	parentID string
	kind     discordgo.ChannelType
}

type Channel struct {
	home          string
	machine       string
	mu            sync.Mutex
	threadMu      sync.Mutex
	cfg           config.Config
	logger        *logx.Logger
	api           api
	apiFactory    func(string) (api, error)
	sleep         func(context.Context, time.Duration) bool
	threads       map[string]threadRecord
	threadIDs     map[string]string
	lastRename    map[string]time.Time
	threadLoadErr error
	channelCache  map[string]channelMeta
	messageRoutes map[int64]messageRoute
	interactions  map[string]*interactionState
}

func New(home string, cfg config.Config) *Channel {
	return newChannel(home, cfg, nil)
}

func newChannel(home string, cfg config.Config, apiClient api) *Channel {
	threads, err := loadThreads(home)
	c := &Channel{
		home:          home,
		machine:       config.MachineName(cfg),
		cfg:           cfg,
		api:           apiClient,
		apiFactory:    newSessionAPI,
		sleep:         sleepContext,
		threads:       threads,
		threadIDs:     map[string]string{},
		lastRename:    map[string]time.Time{},
		threadLoadErr: err,
		channelCache:  map[string]channelMeta{},
		messageRoutes: map[int64]messageRoute{},
		interactions:  map[string]*interactionState{},
	}
	for sessionID, record := range threads {
		c.threadIDs[record.ThreadID] = sessionID
		c.lastRename[sessionID] = record.UpdatedAt
	}
	return c
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Channel) SetLogger(logger *logx.Logger) {
	c.mu.Lock()
	c.logger = logger
	loadErr := c.threadLoadErr
	c.threadLoadErr = nil
	c.mu.Unlock()
	if loadErr != nil && logger != nil {
		logger.Errorf("carregar threads do Discord: %v", loadErr)
	}
}

func (c *Channel) Name() string { return "Discord" }

func (c *Channel) MessageLimit() int { return messageLimit }

func (c *Channel) SupportsProgress() bool { return true }

func (c *Channel) Run(ctx context.Context, handle func(context.Context, channel.Update)) error {
	if handle == nil {
		return errors.New("handler de atualização Discord ausente")
	}
	c.mu.Lock()
	cfg := c.cfg
	factory := c.apiFactory
	sleep := c.sleep
	logger := c.logger
	c.mu.Unlock()
	if cfg.Discord.BotToken == "" {
		return errors.New("token do bot do Discord não configurado")
	}
	client, err := factory(cfg.Discord.BotToken)
	if err != nil {
		return err
	}
	client.SetIntents(discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent)
	c.mu.Lock()
	c.api = client
	c.mu.Unlock()
	client.OnReady(func(session *discordgo.Session, ready *discordgo.Ready) {
		appID := ""
		if session != nil && session.State != nil && session.State.User != nil {
			appID = session.State.User.ID
		}
		if appID == "" && ready != nil && ready.User != nil {
			appID = ready.User.ID
		}
		if appID == "" {
			return
		}
		if _, err := client.ApplicationCommandBulkOverwrite(appID, "", applicationCommands()); err != nil && logger != nil {
			logger.Errorf("registrar comandos do Discord: %v", err)
		}
	})
	client.OnInteraction(func(_ *discordgo.Session, event *discordgo.InteractionCreate) {
		c.handleInteraction(ctx, event, handle)
	})
	client.OnMessage(func(_ *discordgo.Session, event *discordgo.MessageCreate) {
		c.handleMessage(ctx, event, handle)
	})
	if sleep == nil {
		sleep = sleepContext
	}
	backoff := time.Second
	for ctx.Err() == nil {
		if err := client.Open(); err == nil {
			<-ctx.Done()
			return client.Close()
		} else {
			if ctx.Err() != nil {
				return nil
			}
			if logger != nil {
				logger.Errorf("conectar ao Discord: %v", err)
			}
		}
		if !sleep(ctx, backoff) {
			return nil
		}
		if backoff < time.Minute {
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
		}
	}
	return nil
}

func applicationCommands() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{
		{
			Name:        "vincular",
			Description: "Vincula este computador ao canal atual.",
			Options: []*discordgo.ApplicationCommandOption{{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "computador",
				Description: "Nome deste computador",
				Required:    true,
			}},
		},
		{Name: "away", Description: "Ativa o modo ausente."},
		{Name: "back", Description: "Desativa o modo ausente."},
		{Name: "status", Description: "Mostra o estado do agent-bridge."},
		{Name: "list", Description: "Lista as sessões aguardando."},
	}
}

func (c *Channel) Send(ctx context.Context, outgoing channel.Outgoing) (channel.SentMessage, error) {
	text := convertReferenceTags(outgoing.Text)
	chunks := channel.SplitText(text, messageLimit)
	components, err := keyboardComponents(outgoing.Keyboard, c.logError)
	if err != nil {
		return channel.SentMessage{}, err
	}
	if outgoing.Origin != nil {
		if sent, handled, err := c.sendDeferredCommand(outgoing.Origin, chunks, components); handled {
			return sent, err
		}
	}
	client, err := c.apiClient()
	if err != nil {
		return channel.SentMessage{}, err
	}
	destination, err := c.destination(ctx, client, outgoing.Session)
	if err != nil {
		return channel.SentMessage{}, err
	}
	var ids []int64
	retried := false
	for {
		ids = make([]int64, 0, len(chunks))
		restart := false
		for index, chunk := range chunks {
			message := &discordgo.MessageSend{Content: chunk}
			if index == len(chunks)-1 && len(components) > 0 {
				message.Components = components
			}
			if outgoing.ReplyTo != 0 {
				failIfMissing := false
				message.Reference = &discordgo.MessageReference{
					MessageID:       strconv.FormatInt(outgoing.ReplyTo, 10),
					ChannelID:       destination,
					FailIfNotExists: &failIfMissing,
				}
			}
			sent, err := client.ChannelMessageSendComplex(destination, message)
			if err != nil && outgoing.Session.ID != "" && !retried && isUnknownChannel(err) {
				destination, err = c.recreateThread(ctx, client, outgoing.Session)
				if err != nil {
					return channel.SentMessage{}, err
				}
				retried = true
				restart = true
				break
			}
			if err != nil {
				return channel.SentMessage{}, err
			}
			id, err := parseSnowflake(sent.ID)
			if err != nil {
				return channel.SentMessage{}, err
			}
			ids = append(ids, id)
		}
		if !restart {
			break
		}
	}
	c.rememberMessages(ids, destination)
	result := channel.SentMessage{IDs: ids, Chunks: append([]string(nil), chunks...)}
	if len(ids) > 0 {
		result.ID = ids[len(ids)-1]
	}
	return result, nil
}

func (c *Channel) Edit(ctx context.Context, messageID int64, text string, keyboard channel.Keyboard) error {
	converted := convertReferenceTags(text)
	if utf8.RuneCountInString(converted) > messageLimit {
		return errors.New("texto de edição excede o limite do Discord")
	}
	client, channelID, err := c.messageDestination(messageID)
	if err != nil {
		return err
	}
	components, err := keyboardComponents(keyboard, c.logError)
	if err != nil {
		return err
	}
	if components == nil {
		components = []discordgo.MessageComponent{}
	}
	content := converted
	componentPointer := &components
	_, err = client.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:         strconv.FormatInt(messageID, 10),
		Channel:    channelID,
		Content:    &content,
		Components: componentPointer,
	})
	return err
}

func (c *Channel) EditReplyMarkup(ctx context.Context, messageID int64, keyboard channel.Keyboard) error {
	client, channelID, err := c.messageDestination(messageID)
	if err != nil {
		return err
	}
	components, err := keyboardComponents(keyboard, c.logError)
	if err != nil {
		return err
	}
	if components == nil {
		components = []discordgo.MessageComponent{}
	}
	_, err = client.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:         strconv.FormatInt(messageID, 10),
		Channel:    channelID,
		Components: &components,
	})
	return err
}

func (c *Channel) AnswerCallback(ctx context.Context, update channel.Update, text string) error {
	c.mu.Lock()
	state := c.interactions[update.CallbackID]
	client := c.api
	if state == nil || client == nil {
		c.mu.Unlock()
		return errors.New("interação do Discord expirou")
	}
	if state.responded {
		c.mu.Unlock()
		return nil
	}
	state.responded = true
	interaction := state.interaction
	c.mu.Unlock()
	if err := client.InteractionRespond(interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: text, Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		c.markInteractionUnanswered(update.CallbackID)
		return err
	}
	return nil
}

func (c *Channel) RequestText(ctx context.Context, update channel.Update, session channel.SessionRef, prompt, token string) (channel.SentMessage, error) {
	c.mu.Lock()
	state := c.interactions[update.CallbackID]
	client := c.api
	if state != nil && client != nil && !state.responded {
		state.responded = true
		interaction := state.interaction
		c.mu.Unlock()
		label := truncateRunes(prompt, 45)
		placeholder := truncateRunes(prompt, 100)
		err := client.InteractionRespond(interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseModal,
			Data: &discordgo.InteractionResponseData{
				CustomID: token,
				Title:    "Responder",
				Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{CustomID: "texto", Label: label, Placeholder: placeholder, Style: discordgo.TextInputParagraph, Required: true, MaxLength: 4000},
				}}},
			},
		})
		if err != nil {
			c.markInteractionUnanswered(update.CallbackID)
			return channel.SentMessage{}, err
		}
		return channel.SentMessage{}, nil
	}
	c.mu.Unlock()
	return c.Send(ctx, channel.Outgoing{Session: session, Text: prompt})
}

func (c *Channel) apiClient() (api, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api == nil {
		return nil, errors.New("canal Discord indisponível")
	}
	return c.api, nil
}

func (c *Channel) messageDestination(messageID int64) (api, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneMessagesLocked(time.Now())
	route, ok := c.messageRoutes[messageID]
	if !ok {
		return nil, "", errors.New("mensagem Discord desconhecida")
	}
	if c.api == nil {
		return nil, "", errors.New("canal Discord indisponível")
	}
	return c.api, route.channelID, nil
}

func (c *Channel) rememberMessages(ids []int64, channelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.pruneMessagesLocked(now)
	for _, id := range ids {
		c.messageRoutes[id] = messageRoute{channelID: channelID, createdAt: now}
	}
}

func (c *Channel) pruneMessagesLocked(now time.Time) {
	for id, route := range c.messageRoutes {
		if now.Sub(route.createdAt) > 48*time.Hour {
			delete(c.messageRoutes, id)
		}
	}
}

func (c *Channel) markInteractionUnanswered(id string) {
	c.mu.Lock()
	if state := c.interactions[id]; state != nil {
		state.responded = false
	}
	c.mu.Unlock()
}

func (c *Channel) logError(format string, args ...any) {
	c.mu.Lock()
	logger := c.logger
	c.mu.Unlock()
	if logger != nil {
		logger.Errorf(format, args...)
	}
}

func (c *Channel) logErrorLocked(format string, args ...any) {
	if c.logger != nil {
		c.logger.Errorf(format, args...)
	}
}

func parseSnowflake(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ID Discord inválido: %w", err)
	}
	return id, nil
}
