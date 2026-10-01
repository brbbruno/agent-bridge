package discord

import (
	"errors"
	"strconv"
	"sync"

	"github.com/bwmarrin/discordgo"
)

type fakeSend struct {
	channelID string
	message   discordgo.MessageSend
}

type fakeResponse struct {
	interactionID string
	response      discordgo.InteractionResponse
}

type fakeAPI struct {
	mu                       sync.Mutex
	intents                  discordgo.Intent
	openErr                  error
	openErrors               []error
	openCalls                int
	readyRegistrations       int
	interactionRegistrations int
	messageRegistrations     int
	respondErr               error
	closeErr                 error
	openCh                   chan struct{}
	closed                   bool
	nextID                   int64
	readyHandler             func(*discordgo.Session, *discordgo.Ready)
	interactionHandler       func(*discordgo.Session, *discordgo.InteractionCreate)
	messageHandler           func(*discordgo.Session, *discordgo.MessageCreate)
	commands                 []*discordgo.ApplicationCommand
	commandAppID             string
	commandGuildID           string
	sends                    []fakeSend
	sendErrors               []error
	threads                  []*discordgo.Channel
	channelEdits             []*discordgo.ChannelEdit
	messageEdits             []*discordgo.MessageEdit
	channels                 map[string]*discordgo.Channel
	channelLookups           int
	responses                []fakeResponse
	responseEdits            []*discordgo.WebhookEdit
	followups                []*discordgo.WebhookParams
	interactionEditIDs       []string
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{openCh: make(chan struct{}), channels: map[string]*discordgo.Channel{}}
}

func (f *fakeAPI) Open() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls++
	if len(f.openErrors) > 0 {
		err := f.openErrors[0]
		f.openErrors = f.openErrors[1:]
		if err != nil {
			return err
		}
	} else if f.openErr != nil {
		return f.openErr
	}
	select {
	case <-f.openCh:
	default:
		close(f.openCh)
	}
	return nil
}

func (f *fakeAPI) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.closeErr
}

func (f *fakeAPI) SetIntents(intents discordgo.Intent) {
	f.mu.Lock()
	f.intents = intents
	f.mu.Unlock()
}

func (f *fakeAPI) OnReady(handler func(*discordgo.Session, *discordgo.Ready)) {
	f.mu.Lock()
	f.readyRegistrations++
	f.readyHandler = handler
	f.mu.Unlock()
}

func (f *fakeAPI) OnInteraction(handler func(*discordgo.Session, *discordgo.InteractionCreate)) {
	f.mu.Lock()
	f.interactionRegistrations++
	f.interactionHandler = handler
	f.mu.Unlock()
}

func (f *fakeAPI) OnMessage(handler func(*discordgo.Session, *discordgo.MessageCreate)) {
	f.mu.Lock()
	f.messageRegistrations++
	f.messageHandler = handler
	f.mu.Unlock()
}

func (f *fakeAPI) ApplicationCommandBulkOverwrite(appID, guildID string, commands []*discordgo.ApplicationCommand) ([]*discordgo.ApplicationCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commandAppID = appID
	f.commandGuildID = guildID
	f.commands = append([]*discordgo.ApplicationCommand(nil), commands...)
	return commands, nil
}

func (f *fakeAPI) ChannelMessageSendComplex(channelID string, message *discordgo.MessageSend) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy := *message
	copy.Components = append([]discordgo.MessageComponent(nil), message.Components...)
	f.sends = append(f.sends, fakeSend{channelID: channelID, message: copy})
	if len(f.sendErrors) > 0 {
		err := f.sendErrors[0]
		f.sendErrors = f.sendErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	f.nextID++
	id := strconv.FormatInt(f.nextID, 10)
	return &discordgo.Message{ID: id, ChannelID: channelID, Content: message.Content}, nil
}

func (f *fakeAPI) ChannelMessageEditComplex(message *discordgo.MessageEdit) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy := *message
	f.messageEdits = append(f.messageEdits, &copy)
	return &discordgo.Message{ID: message.ID, ChannelID: message.Channel}, nil
}

func (f *fakeAPI) ThreadStartComplex(channelID string, thread *discordgo.ThreadStart) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	created := &discordgo.Channel{ID: "thread-" + strconv.FormatInt(f.nextID, 10), GuildID: "guild", Name: thread.Name, ParentID: channelID, Type: thread.Type}
	f.threads = append(f.threads, created)
	f.channels[created.ID] = created
	return created, nil
}

func (f *fakeAPI) ChannelEdit(channelID string, edit *discordgo.ChannelEdit) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy := *edit
	f.channelEdits = append(f.channelEdits, &copy)
	channel := f.channels[channelID]
	if channel == nil {
		channel = &discordgo.Channel{ID: channelID}
		f.channels[channelID] = channel
	}
	channel.Name = edit.Name
	return channel, nil
}

func (f *fakeAPI) CachedChannel(channelID string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	channel := f.channels[channelID]
	if channel == nil {
		return nil, discordgo.ErrStateNotFound
	}
	copy := *channel
	return &copy, nil
}

func (f *fakeAPI) Channel(channelID string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channelLookups++
	channel := f.channels[channelID]
	if channel == nil {
		return nil, errors.New("unknown channel")
	}
	copy := *channel
	return &copy, nil
}

func (f *fakeAPI) InteractionRespond(interaction *discordgo.Interaction, response *discordgo.InteractionResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.respondErr != nil {
		return f.respondErr
	}
	copy := *response
	if response.Data != nil {
		data := *response.Data
		data.Components = append([]discordgo.MessageComponent(nil), response.Data.Components...)
		copy.Data = &data
	}
	f.responses = append(f.responses, fakeResponse{interactionID: interaction.ID, response: copy})
	return nil
}

func (f *fakeAPI) InteractionResponseEdit(interaction *discordgo.Interaction, edit *discordgo.WebhookEdit) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy := *edit
	f.responseEdits = append(f.responseEdits, &copy)
	f.interactionEditIDs = append(f.interactionEditIDs, interaction.ID)
	f.nextID++
	return &discordgo.Message{ID: strconv.FormatInt(f.nextID, 10), ChannelID: interaction.ChannelID, Content: value(edit.Content)}, nil
}

func (f *fakeAPI) WebhookExecute(_ string, _ string, _ bool, params *discordgo.WebhookParams) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy := *params
	f.followups = append(f.followups, &copy)
	f.nextID++
	return &discordgo.Message{ID: strconv.FormatInt(f.nextID, 10)}, nil
}

func (f *fakeAPI) emitReady(ready *discordgo.Ready) {
	f.mu.Lock()
	handler := f.readyHandler
	f.mu.Unlock()
	if handler != nil {
		handler(nil, ready)
	}
}

func (f *fakeAPI) emitInteraction(event *discordgo.InteractionCreate) {
	f.mu.Lock()
	handler := f.interactionHandler
	f.mu.Unlock()
	if handler != nil {
		handler(nil, event)
	}
}

func (f *fakeAPI) emitMessage(event *discordgo.MessageCreate) {
	f.mu.Lock()
	handler := f.messageHandler
	f.mu.Unlock()
	if handler != nil {
		handler(nil, event)
	}
}

func value(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
