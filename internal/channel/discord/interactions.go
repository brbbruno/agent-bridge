package discord

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/bwmarrin/discordgo"
)

func (c *Channel) handleInteraction(ctx context.Context, event *discordgo.InteractionCreate, handle func(context.Context, channel.Update)) {
	if event == nil || event.Interaction == nil {
		return
	}
	interaction := event.Interaction
	switch interaction.Type {
	case discordgo.InteractionApplicationCommand:
		c.handleApplicationCommand(ctx, interaction, handle)
	case discordgo.InteractionMessageComponent:
		c.handleComponent(ctx, interaction, handle)
	case discordgo.InteractionModalSubmit:
		c.handleModalSubmit(ctx, interaction, handle)
	}
}

func (c *Channel) handleApplicationCommand(ctx context.Context, interaction *discordgo.Interaction, handle func(context.Context, channel.Update)) {
	data := interaction.ApplicationCommandData()
	userID := interactionUserID(interaction)
	if data.Name == "vincular" {
		c.handleBind(ctx, interaction, data, userID)
		return
	}
	if data.Name != "away" && data.Name != "back" && data.Name != "status" && data.Name != "list" {
		return
	}
	client, err := c.apiClient()
	if err != nil {
		c.logError("responder comando Discord: %v", err)
		return
	}
	sessionID, owned := c.ownedChannel(ctx, client, interaction.ChannelID)
	if !owned {
		return
	}
	if !c.allowedUser(userID) {
		c.respondEphemeral(interaction, "Sem permissão.")
		return
	}
	state := c.storeInteraction(interaction, true)
	if err := client.InteractionRespond(interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		c.removeInteraction(interaction.ID)
		c.logError("adiar comando Discord: %v", err)
		return
	}
	c.mu.Lock()
	state.responded = true
	state.deferred = true
	c.mu.Unlock()
	update := channel.Update{
		Channel:    c.Name(),
		UserID:     snowflakeInt(userID),
		SessionID:  sessionID,
		CallbackID: interaction.ID,
		Text:       "/" + data.Name,
	}
	handle(ctx, update)
	c.finishCommand(state)
}

func (c *Channel) handleComponent(ctx context.Context, interaction *discordgo.Interaction, handle func(context.Context, channel.Update)) {
	client, err := c.apiClient()
	if err != nil {
		c.logError("processar interação Discord: %v", err)
		return
	}
	sessionID, owned := c.ownedChannel(ctx, client, interaction.ChannelID)
	if !owned {
		return
	}
	userID := interactionUserID(interaction)
	if !c.allowedUser(userID) {
		c.respondEphemeral(interaction, "Sem permissão.")
		return
	}
	state := c.storeInteraction(interaction, false)
	data := interaction.MessageComponentData()
	messageID := int64(0)
	if interaction.Message != nil {
		messageID = snowflakeInt(interaction.Message.ID)
	}
	handle(ctx, channel.Update{
		Channel:      c.Name(),
		UserID:       snowflakeInt(userID),
		SessionID:    sessionID,
		MessageID:    messageID,
		CallbackID:   interaction.ID,
		CallbackData: data.CustomID,
	})
	c.finishComponent(state)
}

func (c *Channel) handleModalSubmit(ctx context.Context, interaction *discordgo.Interaction, handle func(context.Context, channel.Update)) {
	client, err := c.apiClient()
	if err != nil {
		c.logError("responder formulário Discord: %v", err)
		return
	}
	sessionID, owned := c.ownedChannel(ctx, client, interaction.ChannelID)
	if !owned {
		return
	}
	userID := interactionUserID(interaction)
	if !c.allowedUser(userID) {
		c.respondEphemeral(interaction, "Sem permissão.")
		return
	}
	data := interaction.ModalSubmitData()
	if err := client.InteractionRespond(interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
		c.logError("confirmar formulário Discord: %v", err)
		return
	}
	handle(ctx, channel.Update{
		Channel:   c.Name(),
		UserID:    snowflakeInt(userID),
		SessionID: sessionID,
		TextToken: data.CustomID,
		Text:      modalText(data),
	})
}

func (c *Channel) handleMessage(ctx context.Context, event *discordgo.MessageCreate, handle func(context.Context, channel.Update)) {
	if event == nil || event.Message == nil || event.Author == nil || event.Author.Bot {
		return
	}
	client, err := c.apiClient()
	if err != nil {
		c.logError("processar mensagem Discord: %v", err)
		return
	}
	sessionID, owned := c.ownedChannel(ctx, client, event.ChannelID)
	if !owned || !c.allowedUser(event.Author.ID) {
		return
	}
	messageID := snowflakeInt(event.ID)
	update := channel.Update{
		Channel:   c.Name(),
		ID:        messageID,
		ChatID:    snowflakeInt(event.ChannelID),
		UserID:    snowflakeInt(event.Author.ID),
		MessageID: messageID,
		SessionID: sessionID,
		Text:      event.Content,
	}
	if event.MessageReference != nil {
		update.ReplyToMessage = snowflakeInt(event.MessageReference.MessageID)
	}
	handle(ctx, update)
}

func (c *Channel) handleBind(ctx context.Context, interaction *discordgo.Interaction, data discordgo.ApplicationCommandInteractionData, userID string) {
	option := data.GetOption("computador")
	if option == nil || !strings.EqualFold(option.StringValue(), c.machine) {
		return
	}
	client, err := c.apiClient()
	if err != nil {
		c.logError("validar canal Discord: %v", err)
		return
	}
	remote, err := c.lookupChannel(ctx, client, interaction.ChannelID)
	if err != nil {
		c.respondEphemeral(interaction, "Não foi possível validar este canal.")
		return
	}
	if isThreadType(remote.Type) {
		c.respondEphemeral(interaction, "Use /vincular em um canal de texto, não em uma thread.")
		return
	}
	if remote.Type != discordgo.ChannelTypeGuildText && remote.Type != discordgo.ChannelTypeGuildNews {
		c.respondEphemeral(interaction, "Use /vincular em um canal de texto.")
		return
	}
	if userID == "" {
		c.respondEphemeral(interaction, "Não foi possível identificar o usuário.")
		return
	}
	c.threadMu.Lock()
	c.mu.Lock()
	cfg, err := config.Load(c.home)
	if err == nil && len(cfg.Discord.AllowedUserIDs) > 0 && !containsUser(cfg.Discord.AllowedUserIDs, userID) {
		c.mu.Unlock()
		c.threadMu.Unlock()
		c.respondEphemeral(interaction, "Sem permissão.")
		return
	}
	if err == nil {
		oldChannelID := cfg.Discord.ChannelID
		cfg.Discord.ChannelID = interaction.ChannelID
		cfg.Discord.GuildID = interaction.GuildID
		if len(cfg.Discord.AllowedUserIDs) == 0 {
			cfg.Discord.AllowedUserIDs = []string{userID}
		}
		err = config.Save(c.home, cfg)
		if err == nil {
			c.cfg = cfg
			if oldChannelID != "" && oldChannelID != cfg.Discord.ChannelID {
				c.threads = map[string]threadRecord{}
				c.threadIDs = map[string]string{}
				c.lastRename = map[string]time.Time{}
				c.channelCache = map[string]channelMeta{}
				err = c.saveThreadsLocked()
			}
		}
	}
	c.mu.Unlock()
	c.threadMu.Unlock()
	if err != nil {
		c.logError("salvar vínculo Discord: %v", err)
		c.respondEphemeral(interaction, "Não foi possível salvar o vínculo deste computador.")
		return
	}
	c.respondEphemeral(interaction, fmt.Sprintf("Computador %s vinculado a este canal. As sessões aparecerão em threads aqui.", c.machine))
}

func (c *Channel) ownedChannel(ctx context.Context, client api, channelID string) (string, bool) {
	c.mu.Lock()
	bound := c.cfg.Discord.ChannelID
	sessionID, knownThread := c.threadIDs[channelID]
	c.mu.Unlock()
	if bound == "" || channelID == "" {
		return "", false
	}
	if channelID == bound {
		return "", true
	}
	if knownThread {
		return sessionID, true
	}
	remote, err := c.lookupChannel(ctx, client, channelID)
	if err != nil || !isThreadType(remote.Type) || remote.ParentID != bound {
		return "", false
	}
	c.mu.Lock()
	sessionID = c.threadIDs[channelID]
	c.mu.Unlock()
	return sessionID, true
}

func (c *Channel) lookupChannel(_ context.Context, client api, channelID string) (*discordgo.Channel, error) {
	c.mu.Lock()
	cached, ok := c.channelCache[channelID]
	c.mu.Unlock()
	if ok {
		return &discordgo.Channel{ID: channelID, ParentID: cached.parentID, Type: cached.kind}, nil
	}
	remote, err := client.CachedChannel(channelID)
	if err != nil {
		remote, err = client.Channel(channelID)
	}
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.channelCache[channelID] = channelMeta{parentID: remote.ParentID, kind: remote.Type}
	c.mu.Unlock()
	return remote, nil
}

func (c *Channel) allowedUser(userID string) bool {
	c.mu.Lock()
	allowed := append([]string(nil), c.cfg.Discord.AllowedUserIDs...)
	c.mu.Unlock()
	if len(allowed) == 0 {
		return true
	}
	return containsUser(allowed, userID)
}

func containsUser(users []string, target string) bool {
	for _, user := range users {
		if user == target {
			return true
		}
	}
	return false
}

func interactionUserID(interaction *discordgo.Interaction) string {
	if interaction.Member != nil && interaction.Member.User != nil {
		return interaction.Member.User.ID
	}
	if interaction.User != nil {
		return interaction.User.ID
	}
	return ""
}

func snowflakeInt(value string) int64 {
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}

func (c *Channel) respondEphemeral(interaction *discordgo.Interaction, text string) {
	client, err := c.apiClient()
	if err != nil {
		return
	}
	if err := client.InteractionRespond(interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: text, Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		c.logError("responder interação Discord: %v", err)
	}
}

func (c *Channel) storeInteraction(interaction *discordgo.Interaction, command bool) *interactionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneInteractionsLocked(time.Now())
	state := &interactionState{interaction: interaction, createdAt: time.Now(), command: command}
	c.interactions[interaction.ID] = state
	return state
}

func (c *Channel) pruneInteractionsLocked(now time.Time) {
	for id, state := range c.interactions {
		if now.Sub(state.createdAt) > 15*time.Minute {
			delete(c.interactions, id)
		}
	}
}

func (c *Channel) removeInteraction(id string) {
	c.mu.Lock()
	delete(c.interactions, id)
	c.mu.Unlock()
}

func (c *Channel) finishComponent(state *interactionState) {
	if state == nil {
		return
	}
	c.mu.Lock()
	if state.responded {
		c.mu.Unlock()
		return
	}
	state.responded = true
	interaction := state.interaction
	client := c.api
	c.mu.Unlock()
	if client != nil {
		if err := client.InteractionRespond(interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
			c.logError("confirmar interação Discord: %v", err)
		}
	}
}

func (c *Channel) finishCommand(state *interactionState) {
	if state == nil {
		return
	}
	c.mu.Lock()
	if state.replied {
		c.mu.Unlock()
		return
	}
	state.replied = true
	interaction := state.interaction
	client := c.api
	c.mu.Unlock()
	if client == nil {
		return
	}
	text := "Comando recebido."
	if _, err := client.InteractionResponseEdit(interaction, &discordgo.WebhookEdit{Content: &text}); err != nil {
		c.logError("finalizar comando Discord: %v", err)
	}
}

func (c *Channel) sendDeferredCommand(update *channel.Update, chunks []string, components []discordgo.MessageComponent) (channel.SentMessage, bool, error) {
	if update == nil || update.CallbackID == "" {
		return channel.SentMessage{}, false, nil
	}
	c.mu.Lock()
	state := c.interactions[update.CallbackID]
	client := c.api
	if state == nil || !state.command || !state.deferred {
		c.mu.Unlock()
		return channel.SentMessage{}, false, nil
	}
	if state.replied {
		c.mu.Unlock()
		return channel.SentMessage{}, true, nil
	}
	interaction := state.interaction
	c.mu.Unlock()
	if client == nil {
		return channel.SentMessage{}, true, errors.New("canal Discord indisponível")
	}
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	content := chunks[0]
	edit := &discordgo.WebhookEdit{Content: &content}
	if components != nil {
		edit.Components = &components
	}
	message, err := client.InteractionResponseEdit(interaction, edit)
	if err != nil {
		return channel.SentMessage{}, true, err
	}
	c.mu.Lock()
	state.replied = true
	c.mu.Unlock()
	result := channel.SentMessage{Chunks: append([]string(nil), chunks...)}
	if message != nil {
		if id, parseErr := parseSnowflake(message.ID); parseErr == nil {
			result.ID = id
			result.IDs = append(result.IDs, id)
		}
	}
	for _, chunk := range chunks[1:] {
		followup, err := client.WebhookExecute(interaction.AppID, interaction.Token, true, &discordgo.WebhookParams{Content: chunk, Flags: discordgo.MessageFlagsEphemeral})
		if err != nil {
			return result, true, err
		}
		if followup != nil {
			if id, parseErr := parseSnowflake(followup.ID); parseErr == nil {
				result.IDs = append(result.IDs, id)
				result.ID = id
			}
		}
	}
	return result, true, nil
}

func modalText(data discordgo.ModalSubmitInteractionData) string {
	for _, component := range data.Components {
		var items []discordgo.MessageComponent
		switch row := component.(type) {
		case discordgo.ActionsRow:
			items = row.Components
		case *discordgo.ActionsRow:
			items = row.Components
		}
		for _, item := range items {
			switch input := item.(type) {
			case discordgo.TextInput:
				if input.CustomID == "texto" {
					return input.Value
				}
			case *discordgo.TextInput:
				if input.CustomID == "texto" {
					return input.Value
				}
			}
		}
	}
	return ""
}

func isThreadType(kind discordgo.ChannelType) bool {
	return kind == discordgo.ChannelTypeGuildNewsThread || kind == discordgo.ChannelTypeGuildPublicThread || kind == discordgo.ChannelTypeGuildPrivateThread
}
