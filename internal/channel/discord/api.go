package discord

import "github.com/bwmarrin/discordgo"

type api interface {
	Open() error
	Close() error
	SetIntents(discordgo.Intent)
	OnReady(func(*discordgo.Session, *discordgo.Ready))
	OnInteraction(func(*discordgo.Session, *discordgo.InteractionCreate))
	OnMessage(func(*discordgo.Session, *discordgo.MessageCreate))
	ApplicationCommandBulkOverwrite(string, string, []*discordgo.ApplicationCommand) ([]*discordgo.ApplicationCommand, error)
	ChannelMessageSendComplex(string, *discordgo.MessageSend) (*discordgo.Message, error)
	ChannelMessageEditComplex(*discordgo.MessageEdit) (*discordgo.Message, error)
	ThreadStartComplex(string, *discordgo.ThreadStart) (*discordgo.Channel, error)
	ChannelEdit(string, *discordgo.ChannelEdit) (*discordgo.Channel, error)
	CachedChannel(string) (*discordgo.Channel, error)
	Channel(string) (*discordgo.Channel, error)
	InteractionRespond(*discordgo.Interaction, *discordgo.InteractionResponse) error
	InteractionResponseEdit(*discordgo.Interaction, *discordgo.WebhookEdit) (*discordgo.Message, error)
	WebhookExecute(string, string, bool, *discordgo.WebhookParams) (*discordgo.Message, error)
}

type sessionAPI struct {
	session *discordgo.Session
}

func newSessionAPI(token string) (api, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	return &sessionAPI{session: session}, nil
}

func (a *sessionAPI) Open() error                         { return a.session.Open() }
func (a *sessionAPI) Close() error                        { return a.session.Close() }
func (a *sessionAPI) SetIntents(intents discordgo.Intent) { a.session.Identify.Intents = intents }
func (a *sessionAPI) OnReady(handler func(*discordgo.Session, *discordgo.Ready)) {
	a.session.AddHandler(handler)
}
func (a *sessionAPI) OnInteraction(handler func(*discordgo.Session, *discordgo.InteractionCreate)) {
	a.session.AddHandler(handler)
}
func (a *sessionAPI) OnMessage(handler func(*discordgo.Session, *discordgo.MessageCreate)) {
	a.session.AddHandler(handler)
}
func (a *sessionAPI) ApplicationCommandBulkOverwrite(appID, guildID string, commands []*discordgo.ApplicationCommand) ([]*discordgo.ApplicationCommand, error) {
	return a.session.ApplicationCommandBulkOverwrite(appID, guildID, commands)
}
func (a *sessionAPI) ChannelMessageSendComplex(channelID string, message *discordgo.MessageSend) (*discordgo.Message, error) {
	return a.session.ChannelMessageSendComplex(channelID, message)
}
func (a *sessionAPI) ChannelMessageEditComplex(message *discordgo.MessageEdit) (*discordgo.Message, error) {
	return a.session.ChannelMessageEditComplex(message)
}
func (a *sessionAPI) ThreadStartComplex(channelID string, thread *discordgo.ThreadStart) (*discordgo.Channel, error) {
	return a.session.ThreadStartComplex(channelID, thread)
}
func (a *sessionAPI) ChannelEdit(channelID string, edit *discordgo.ChannelEdit) (*discordgo.Channel, error) {
	return a.session.ChannelEdit(channelID, edit)
}
func (a *sessionAPI) CachedChannel(channelID string) (*discordgo.Channel, error) {
	if a.session.State == nil {
		return nil, discordgo.ErrStateNotFound
	}
	return a.session.State.Channel(channelID)
}
func (a *sessionAPI) Channel(channelID string) (*discordgo.Channel, error) {
	return a.session.Channel(channelID)
}
func (a *sessionAPI) InteractionRespond(interaction *discordgo.Interaction, response *discordgo.InteractionResponse) error {
	return a.session.InteractionRespond(interaction, response)
}
func (a *sessionAPI) InteractionResponseEdit(interaction *discordgo.Interaction, edit *discordgo.WebhookEdit) (*discordgo.Message, error) {
	return a.session.InteractionResponseEdit(interaction, edit)
}
func (a *sessionAPI) WebhookExecute(appID, token string, wait bool, params *discordgo.WebhookParams) (*discordgo.Message, error) {
	return a.session.WebhookExecute(appID, token, wait, params)
}
