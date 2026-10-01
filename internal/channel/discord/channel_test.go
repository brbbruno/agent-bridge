package discord

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/logx"
	"github.com/bwmarrin/discordgo"
)

func testBoundChannel(t *testing.T, client *fakeAPI) (*Channel, string) {
	t.Helper()
	cfg := config.Default()
	cfg.MachineName = "PC-TESTE"
	cfg.Discord.BotToken = "fake-token"
	cfg.Discord.GuildID = "guild"
	cfg.Discord.ChannelID = "bound"
	cfg.Discord.AllowedUserIDs = []string{"owner"}
	client.channels["bound"] = &discordgo.Channel{ID: "bound", GuildID: "guild", Type: discordgo.ChannelTypeGuildText}
	home := t.TempDir()
	channel := newChannel(home, cfg, client)
	channel.SetLogger(logx.New(filepath.Join(home, "discord.log")))
	return channel, home
}

func commandInteraction(id, name, channelID, guildID, userID string, options ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	for _, option := range options {
		option.Type = discordgo.ApplicationCommandOptionString
	}
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: id, AppID: "application", Type: discordgo.InteractionApplicationCommand,
		ChannelID: channelID, GuildID: guildID,
		Member: &discordgo.Member{User: &discordgo.User{ID: userID}},
		Data:   discordgo.ApplicationCommandInteractionData{Name: name, Options: options},
	}}
}

func TestKeyboardPackingStylesAndLimits(t *testing.T) {
	client := newFakeAPI()
	discord, home := testBoundChannel(t, client)
	buttons := make([]channel.Button, 6)
	for index := range buttons {
		buttons[index] = channel.Button{Text: "Botão", Data: "button-" + string(rune('a'+index))}
	}
	buttons[0].Style = "success"
	buttons[1].Style = "danger"
	buttons[2].Style = "primary"
	buttons[3].Text = strings.Repeat("x", 90)
	if _, err := discord.Send(context.Background(), channel.Outgoing{Text: "opções", Keyboard: channel.Keyboard{buttons}}); err != nil {
		t.Fatal(err)
	}
	if len(client.sends) != 1 || len(client.sends[0].message.Components) != 2 {
		t.Fatalf("linhas de botões=%+v", client.sends)
	}
	first := client.sends[0].message.Components[0].(discordgo.ActionsRow)
	second := client.sends[0].message.Components[1].(discordgo.ActionsRow)
	if len(first.Components) != 5 || len(second.Components) != 1 {
		t.Fatalf("linhas com tamanhos inesperados: %d, %d", len(first.Components), len(second.Components))
	}
	button := first.Components[0].(discordgo.Button)
	if button.Style != discordgo.SuccessButton {
		t.Fatalf("estilo success=%v", button.Style)
	}
	button = first.Components[1].(discordgo.Button)
	if button.Style != discordgo.DangerButton {
		t.Fatalf("estilo danger=%v", button.Style)
	}
	button = first.Components[2].(discordgo.Button)
	if button.Style != discordgo.PrimaryButton {
		t.Fatalf("estilo primary=%v", button.Style)
	}
	button = first.Components[3].(discordgo.Button)
	if utf8.RuneCountInString(button.Label) != 80 {
		t.Fatalf("label não foi truncado: %d", utf8.RuneCountInString(button.Label))
	}
	if button = first.Components[4].(discordgo.Button); button.Style != discordgo.SecondaryButton {
		t.Fatalf("estilo padrão=%v", button.Style)
	}

	tooMany := make([]channel.Button, 26)
	for index := range tooMany {
		tooMany[index] = channel.Button{Text: "Opção", Data: "extra-" + string(rune('a'+index))}
	}
	if _, err := discord.Send(context.Background(), channel.Outgoing{Text: "muitas opções", Keyboard: channel.Keyboard{tooMany}}); err != nil {
		t.Fatal(err)
	}
	if got := len(client.sends[1].message.Components); got != 5 {
		t.Fatalf("linhas para 26 botões=%d", got)
	}
	last := client.sends[1].message.Components[4].(discordgo.ActionsRow)
	if len(last.Components) != 5 {
		t.Fatalf("última linha tem %d botões", len(last.Components))
	}
	invalid := channel.Keyboard{{{Text: "inválido", Data: strings.Repeat("x", 101)}}}
	if _, err := discord.Send(context.Background(), channel.Outgoing{Text: "não enviar", Keyboard: invalid}); err == nil {
		t.Fatal("custom_id com mais de 100 bytes foi aceito")
	}
	if len(client.sends) != 2 {
		t.Fatalf("custom_id inválido enviou mensagem: %d", len(client.sends))
	}
	logData, err := os.ReadFile(filepath.Join(home, "discord.log"))
	if err != nil || !strings.Contains(string(logData), "botões excedentes ignorados") {
		t.Fatalf("extras não foram registrados no log: %s, err=%v", logData, err)
	}
}

func TestSendConvertsReferenceTagsAndStoresMessageRoute(t *testing.T) {
	client := newFakeAPI()
	discord, _ := testBoundChannel(t, client)
	source := `<ref_file file="C:\a\b\router.go" /> <ref_snippet file="C:\a\b\router.go" lines="2-4" />`
	message, err := discord.Send(context.Background(), channel.Outgoing{Text: source})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.sends[0].message.Content; got != "`router.go` `router.go:2-4`" {
		t.Fatalf("referências convertidas=%q", got)
	}
	if message.ID == 0 || len(message.Chunks) != 1 || message.Chunks[0] != "`router.go` `router.go:2-4`" {
		t.Fatalf("sent message=%+v", message)
	}
	keyboard := channel.Keyboard{{{Text: "ok", Data: "ok"}}}
	if err := discord.Edit(context.Background(), message.ID, "atualizado", keyboard); err != nil {
		t.Fatal(err)
	}
	if len(client.messageEdits) != 1 || *client.messageEdits[0].Content != "atualizado" {
		t.Fatalf("edição=%+v", client.messageEdits)
	}
	if err := discord.EditReplyMarkup(context.Background(), message.ID, nil); err != nil {
		t.Fatal(err)
	}
	if client.messageEdits[1].Components == nil || len(*client.messageEdits[1].Components) != 0 {
		t.Fatalf("markup não foi limpo com componentes vazios: %+v", client.messageEdits[1])
	}
	if err := discord.Edit(context.Background(), 9999, "não existe", nil); err == nil {
		t.Fatal("edição de ID desconhecido foi aceita")
	}
}

func TestThreadCreationPersistenceReuseAndName(t *testing.T) {
	client := newFakeAPI()
	discord, home := testBoundChannel(t, client)
	ref := channel.SessionRef{ID: "session-1", Name: "task-id", Title: "Implementar Discord"}
	first, err := discord.Send(context.Background(), channel.Outgoing{Session: ref, Text: "primeiro"})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.threads) != 1 || client.threads[0].ParentID != "bound" || client.threads[0].Type != discordgo.ChannelTypeGuildPublicThread || client.threads[0].Name != "Implementar Discord (task-id)" {
		t.Fatalf("thread criada=%+v", client.threads)
	}
	if client.sends[0].channelID != client.threads[0].ID || first.ID == 0 {
		t.Fatalf("destino da primeira mensagem=%+v", client.sends[0])
	}
	persisted, err := loadThreads(home)
	if err != nil || persisted[ref.ID].ThreadID != client.threads[0].ID {
		t.Fatalf("threads não persistidas: %+v err=%v", persisted, err)
	}
	info, err := os.Stat(filepath.Join(home, "discord-threads.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("permissões do arquivo=%o", info.Mode().Perm())
	}

	reloaded := newChannel(home, discord.cfg, client)
	if _, err := reloaded.Send(context.Background(), channel.Outgoing{Session: ref, Text: "segundo"}); err != nil {
		t.Fatal(err)
	}
	if len(client.threads) != 1 || client.sends[1].channelID != client.threads[0].ID {
		t.Fatalf("thread persistida não foi reutilizada: threads=%d sends=%+v", len(client.threads), client.sends)
	}
	if got := threadName(channel.SessionRef{ID: "id", Name: "nome", Title: strings.Repeat("á", 120)}); utf8.RuneCountInString(got) != 100 {
		t.Fatalf("nome da thread tem %d runes", utf8.RuneCountInString(got))
	}
}

func TestThreadRenameIsThrottled(t *testing.T) {
	client := newFakeAPI()
	discord, _ := testBoundChannel(t, client)
	ref := channel.SessionRef{ID: "session", Name: "name", Title: "Antes"}
	if _, err := discord.Send(context.Background(), channel.Outgoing{Session: ref, Text: "um"}); err != nil {
		t.Fatal(err)
	}
	discord.mu.Lock()
	discord.lastRename[ref.ID] = time.Now().Add(-5 * time.Minute)
	discord.mu.Unlock()
	ref.Title = "Depois"
	if _, err := discord.Send(context.Background(), channel.Outgoing{Session: ref, Text: "dois"}); err != nil {
		t.Fatal(err)
	}
	if len(client.channelEdits) != 0 {
		t.Fatalf("rename ocorreu antes de dez minutos: %+v", client.channelEdits)
	}
	discord.mu.Lock()
	discord.lastRename[ref.ID] = time.Now().Add(-11 * time.Minute)
	discord.mu.Unlock()
	if _, err := discord.Send(context.Background(), channel.Outgoing{Session: ref, Text: "três"}); err != nil {
		t.Fatal(err)
	}
	if len(client.channelEdits) != 1 || client.channelEdits[0].Name != "Depois (name)" {
		t.Fatalf("rename da thread=%+v", client.channelEdits)
	}
}

func TestUnknownThreadChannelIsRecreatedOnce(t *testing.T) {
	client := newFakeAPI()
	discord, _ := testBoundChannel(t, client)
	ref := channel.SessionRef{ID: "session", Name: "task"}
	if _, err := discord.Send(context.Background(), channel.Outgoing{Session: ref, Text: "primeiro"}); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.sendErrors = append(client.sendErrors, &discordgo.RESTError{Message: &discordgo.APIErrorMessage{Code: 10003, Message: "Unknown Channel"}})
	client.mu.Unlock()
	if _, err := discord.Send(context.Background(), channel.Outgoing{Session: ref, Text: "recriar"}); err != nil {
		t.Fatal(err)
	}
	if len(client.threads) != 2 || client.sends[len(client.sends)-1].channelID != client.threads[1].ID {
		t.Fatalf("thread não recriada: threads=%+v sends=%+v", client.threads, client.sends)
	}
}

func TestUnboundAndMachineLevelSend(t *testing.T) {
	client := newFakeAPI()
	cfg := config.Default()
	cfg.MachineName = "PC-TESTE"
	discord := newChannel(t.TempDir(), cfg, client)
	if _, err := discord.Send(context.Background(), channel.Outgoing{Text: "sem vínculo"}); err == nil || !strings.Contains(err.Error(), "use /vincular computador:PC-TESTE") {
		t.Fatalf("erro de canal não vinculado=%v", err)
	}
	discord, _ = testBoundChannel(t, client)
	if _, err := discord.Send(context.Background(), channel.Outgoing{Text: "máquina"}); err != nil {
		t.Fatal(err)
	}
	if client.sends[0].channelID != "bound" {
		t.Fatalf("mensagem sem sessão foi enviada a %q", client.sends[0].channelID)
	}
}

func TestOwnershipAllowsBoundChannelAndItsThreadsOnly(t *testing.T) {
	client := newFakeAPI()
	discord, _ := testBoundChannel(t, client)
	client.channels["outside"] = &discordgo.Channel{ID: "outside", Type: discordgo.ChannelTypeGuildText}
	client.channels["thread"] = &discordgo.Channel{ID: "thread", ParentID: "bound", Type: discordgo.ChannelTypeGuildPublicThread}
	discord.threadIDs["thread"] = "session"
	var updates []channel.Update
	handle := func(_ context.Context, update channel.Update) { updates = append(updates, update) }
	discord.handleMessage(context.Background(), &discordgo.MessageCreate{Message: &discordgo.Message{ID: "1", ChannelID: "outside", Author: &discordgo.User{ID: "owner"}, Content: "ignorar"}}, handle)
	discord.handleMessage(context.Background(), &discordgo.MessageCreate{Message: &discordgo.Message{ID: "2", ChannelID: "thread", Author: &discordgo.User{ID: "owner"}, Content: "thread"}}, handle)
	discord.handleMessage(context.Background(), &discordgo.MessageCreate{Message: &discordgo.Message{ID: "3", ChannelID: "bound", Author: &discordgo.User{ID: "other"}, Content: "ignorar"}}, handle)
	if len(updates) != 1 || updates[0].Channel != "Discord" || updates[0].SessionID != "session" || updates[0].Text != "thread" {
		t.Fatalf("mensagens roteadas=%+v", updates)
	}

	interaction := commandInteraction("not-allowed", "status", "bound", "guild", "other")
	discord.handleInteraction(context.Background(), interaction, handle)
	if len(client.responses) != 1 || client.responses[0].response.Type != discordgo.InteractionResponseChannelMessageWithSource || client.responses[0].response.Data.Content != "Sem permissão." || client.responses[0].response.Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Fatalf("resposta a usuário não autorizado=%+v", client.responses)
	}
}

func TestVincularMachineAndAllowlistedRebinding(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.MachineName = "PC-TESTE"
	cfg.Discord.BotToken = "token"
	if err := config.Save(home, cfg); err != nil {
		t.Fatal(err)
	}
	client := newFakeAPI()
	client.channels["text"] = &discordgo.Channel{ID: "text", GuildID: "guild", Type: discordgo.ChannelTypeGuildText}
	client.channels["other"] = &discordgo.Channel{ID: "other", GuildID: "guild", Type: discordgo.ChannelTypeGuildText}
	discord := newChannel(home, cfg, client)
	discord.SetLogger(logx.New(filepath.Join(home, "discord.log")))
	var handled []channel.Update
	handle := func(_ context.Context, update channel.Update) { handled = append(handled, update) }

	discord.handleInteraction(context.Background(), commandInteraction("wrong-machine", "vincular", "text", "guild", "owner", &discordgo.ApplicationCommandInteractionDataOption{Name: "computador", Value: "outro-pc"}), handle)
	if len(client.responses) != 0 {
		t.Fatalf("vínculo de outro computador respondeu: %+v", client.responses)
	}
	discord.handleInteraction(context.Background(), commandInteraction("bind", "vincular", "text", "guild", "owner", &discordgo.ApplicationCommandInteractionDataOption{Name: "computador", Value: "pc-teste"}), handle)
	loaded, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Discord.ChannelID != "text" || loaded.Discord.GuildID != "guild" || len(loaded.Discord.AllowedUserIDs) != 1 || loaded.Discord.AllowedUserIDs[0] != "owner" {
		t.Fatalf("vínculo não persistido: %+v", loaded.Discord)
	}
	if got := client.responses[len(client.responses)-1].response.Data.Content; got != "Computador PC-TESTE vinculado a este canal. As sessões aparecerão em threads aqui." {
		t.Fatalf("confirmação de vínculo=%q", got)
	}

	discord.handleInteraction(context.Background(), commandInteraction("rebind-denied", "vincular", "other", "guild", "other-user", &discordgo.ApplicationCommandInteractionDataOption{Name: "computador", Value: "PC-TESTE"}), handle)
	loaded, err = config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Discord.ChannelID != "text" || client.responses[len(client.responses)-1].response.Data.Content != "Sem permissão." {
		t.Fatalf("rebind não autorizado alterou configuração: %+v responses=%+v", loaded.Discord, client.responses)
	}

	client.channels["thread"] = &discordgo.Channel{ID: "thread", GuildID: "guild", ParentID: "text", Type: discordgo.ChannelTypeGuildPublicThread}
	discord.handleInteraction(context.Background(), commandInteraction("bind-in-thread", "vincular", "thread", "guild", "owner", &discordgo.ApplicationCommandInteractionDataOption{Name: "computador", Value: "PC-TESTE"}), handle)
	if got := client.responses[len(client.responses)-1].response.Data.Content; got != "Use /vincular em um canal de texto, não em uma thread." {
		t.Fatalf("erro de vínculo em thread=%q", got)
	}
}

func TestComponentCallbackModalAndSafetyAcknowledgement(t *testing.T) {
	client := newFakeAPI()
	discord, _ := testBoundChannel(t, client)
	client.channels["thread"] = &discordgo.Channel{ID: "thread", GuildID: "guild", ParentID: "bound", Type: discordgo.ChannelTypeGuildPublicThread}
	discord.threadIDs["thread"] = "session"
	var received channel.Update
	component := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "component", Type: discordgo.InteractionMessageComponent, ChannelID: "thread", GuildID: "guild",
		Member:  &discordgo.Member{User: &discordgo.User{ID: "owner"}},
		Message: &discordgo.Message{ID: "44", ChannelID: "thread"},
		Data:    discordgo.MessageComponentInteractionData{CustomID: "q:pending:0:o", ComponentType: discordgo.ButtonComponent},
	}}
	discord.handleInteraction(context.Background(), component, func(ctx context.Context, update channel.Update) {
		received = update
		if err := discord.AnswerCallback(ctx, update, "Resposta recebida."); err != nil {
			t.Error(err)
		}
	})
	if received.Channel != "Discord" || received.SessionID != "session" || received.MessageID != 44 || received.CallbackData != "q:pending:0:o" {
		t.Fatalf("update de componente=%+v", received)
	}
	if len(client.responses) != 1 || client.responses[0].response.Type != discordgo.InteractionResponseChannelMessageWithSource || client.responses[0].response.Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Fatalf("acknowledgement de componente=%+v", client.responses)
	}

	unanswered := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "safety", Type: discordgo.InteractionMessageComponent, ChannelID: "bound", GuildID: "guild",
		Member: &discordgo.Member{User: &discordgo.User{ID: "owner"}},
		Data:   discordgo.MessageComponentInteractionData{CustomID: "unknown", ComponentType: discordgo.ButtonComponent},
	}}
	discord.handleInteraction(context.Background(), unanswered, func(context.Context, channel.Update) {})
	if got := client.responses[1].response.Type; got != discordgo.InteractionResponseDeferredMessageUpdate {
		t.Fatalf("resposta de segurança=%v", got)
	}
}

func TestRequestTextModalAndSubmission(t *testing.T) {
	client := newFakeAPI()
	discord, _ := testBoundChannel(t, client)
	client.channels["thread"] = &discordgo.Channel{ID: "thread", GuildID: "guild", ParentID: "bound", Type: discordgo.ChannelTypeGuildPublicThread}
	discord.threadIDs["thread"] = "session"
	prompt := strings.Repeat("á", 120)
	component := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "other", Type: discordgo.InteractionMessageComponent, ChannelID: "thread", GuildID: "guild",
		Member: &discordgo.Member{User: &discordgo.User{ID: "owner"}},
		Data:   discordgo.MessageComponentInteractionData{CustomID: "q:p:0:o", ComponentType: discordgo.ButtonComponent},
	}}
	var sent channel.SentMessage
	discord.handleInteraction(context.Background(), component, func(ctx context.Context, update channel.Update) {
		var err error
		sent, err = discord.RequestText(ctx, update, channel.SessionRef{ID: "session"}, prompt, "t:p")
		if err != nil {
			t.Error(err)
		}
	})
	if sent.ID != 0 || len(sent.IDs) != 0 {
		t.Fatalf("modal deveria retornar mensagem vazia: %+v", sent)
	}
	modal := client.responses[len(client.responses)-1].response
	if modal.Type != discordgo.InteractionResponseModal || modal.Data.CustomID != "t:p" || modal.Data.Title != "Responder" {
		t.Fatalf("modal=%+v", modal)
	}
	row := modal.Data.Components[0].(discordgo.ActionsRow)
	input := row.Components[0].(discordgo.TextInput)
	if input.CustomID != "texto" || input.Style != discordgo.TextInputParagraph || !input.Required || input.MaxLength != 4000 || utf8.RuneCountInString(input.Label) != 45 || utf8.RuneCountInString(input.Placeholder) != 100 {
		t.Fatalf("TextInput=%+v", input)
	}

	var submitted channel.Update
	modalSubmit := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "submit", Type: discordgo.InteractionModalSubmit, ChannelID: "thread", GuildID: "guild",
		Member: &discordgo.Member{User: &discordgo.User{ID: "owner"}},
		Data:   discordgo.ModalSubmitInteractionData{CustomID: "t:p", Components: []discordgo.MessageComponent{&discordgo.ActionsRow{Components: []discordgo.MessageComponent{&discordgo.TextInput{CustomID: "texto", Value: "Roxo"}}}}},
	}}
	discord.handleInteraction(context.Background(), modalSubmit, func(_ context.Context, update channel.Update) { submitted = update })
	if submitted.Channel != "Discord" || submitted.SessionID != "session" || submitted.TextToken != "t:p" || submitted.Text != "Roxo" {
		t.Fatalf("update do modal=%+v", submitted)
	}
	if got := client.responses[len(client.responses)-1].response.Type; got != discordgo.InteractionResponseDeferredMessageUpdate {
		t.Fatalf("modal submit não foi confirmado cedo: %v", got)
	}
}

func TestSlashCommandEditsDeferredResponseOrUsesFallback(t *testing.T) {
	client := newFakeAPI()
	discord, _ := testBoundChannel(t, client)
	status := commandInteraction("status", "status", "bound", "guild", "owner")
	discord.handleInteraction(context.Background(), status, func(ctx context.Context, update channel.Update) {
		if update.Text != "/status" || update.CallbackID != "status" {
			t.Fatalf("update de slash command=%+v", update)
		}
		if _, err := discord.Send(ctx, channel.Outgoing{Text: "Status do roteador", Origin: &update}); err != nil {
			t.Error(err)
		}
	})
	if len(client.responses) != 1 || client.responses[0].response.Type != discordgo.InteractionResponseDeferredChannelMessageWithSource || client.responses[0].response.Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Fatalf("defer do comando=%+v", client.responses)
	}
	if len(client.responseEdits) != 1 || value(client.responseEdits[0].Content) != "Status do roteador" || len(client.sends) != 0 {
		t.Fatalf("resposta do comando não editou interação: edits=%+v sends=%+v", client.responseEdits, client.sends)
	}

	discord.handleInteraction(context.Background(), commandInteraction("list", "list", "bound", "guild", "owner"), func(context.Context, channel.Update) {})
	if len(client.responseEdits) != 2 || value(client.responseEdits[1].Content) != "Comando recebido." {
		t.Fatalf("fallback de comando=%+v", client.responseEdits)
	}
	discord.handleInteraction(context.Background(), commandInteraction("long-list", "list", "bound", "guild", "owner"), func(ctx context.Context, update channel.Update) {
		if _, err := discord.Send(ctx, channel.Outgoing{Text: strings.Repeat("x", 2100), Origin: &update}); err != nil {
			t.Error(err)
		}
	})
	if len(client.responseEdits) != 3 || len(client.followups) != 1 || client.followups[0].Flags != discordgo.MessageFlagsEphemeral || len(client.sends) != 0 {
		t.Fatalf("comando longo não usou resposta e follow-up efêmero: edits=%+v followups=%+v sends=%+v", client.responseEdits, client.followups, client.sends)
	}
}

func TestRunRetriesOpenAndRegistersHandlersOnce(t *testing.T) {
	client := newFakeAPI()
	client.openErrors = []error{errors.New("offline"), errors.New("offline")}
	cfg := config.Default()
	cfg.Discord.BotToken = "fake-token"
	discord := newChannel(t.TempDir(), cfg, nil)
	logPath := filepath.Join(t.TempDir(), "discord.log")
	discord.SetLogger(logx.New(logPath))
	discord.apiFactory = func(string) (api, error) { return client, nil }
	var delays []time.Duration
	discord.sleep = func(ctx context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return ctx.Err() == nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- discord.Run(ctx, func(context.Context, channel.Update) {}) }()
	select {
	case <-client.openCh:
	case <-time.After(time.Second):
		t.Fatal("sessão fake não abriu após novas tentativas")
	}
	client.emitReady(&discordgo.Ready{User: &discordgo.User{ID: "application"}})
	client.mu.Lock()
	intents := client.intents
	openCalls := client.openCalls
	registrations := []int{client.readyRegistrations, client.interactionRegistrations, client.messageRegistrations}
	appID, guildID := client.commandAppID, client.commandGuildID
	commands := append([]*discordgo.ApplicationCommand(nil), client.commands...)
	client.mu.Unlock()
	if intents != discordgo.IntentsGuilds|discordgo.IntentsGuildMessages|discordgo.IntentsMessageContent {
		t.Fatalf("intents=%v", intents)
	}
	if openCalls != 3 || len(delays) != 2 || delays[0] != time.Second || delays[1] != 2*time.Second {
		t.Fatalf("tentativas=%d backoffs=%v", openCalls, delays)
	}
	if registrations[0] != 1 || registrations[1] != 1 || registrations[2] != 1 {
		t.Fatalf("handlers registrados mais de uma vez: %v", registrations)
	}
	if appID != "application" || guildID != "" || len(commands) != 5 {
		t.Fatalf("comandos globais: app=%q guild=%q commands=%+v", appID, guildID, commands)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(logData), "conectar ao Discord: offline") != 2 {
		t.Fatalf("falhas de conexão não foram registradas: %s err=%v", logData, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !client.closed {
		t.Fatal("sessão Discord não foi fechada")
	}
}

func TestRunReturnsPromptlyWhenContextCancelsDuringBackoff(t *testing.T) {
	client := newFakeAPI()
	client.openErrors = []error{errors.New("offline")}
	cfg := config.Default()
	cfg.Discord.BotToken = "fake-token"
	discord := newChannel(t.TempDir(), cfg, nil)
	discord.apiFactory = func(string) (api, error) { return client, nil }
	backoff := make(chan time.Duration, 1)
	discord.sleep = func(ctx context.Context, delay time.Duration) bool {
		backoff <- delay
		<-ctx.Done()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- discord.Run(ctx, func(context.Context, channel.Update) {}) }()
	select {
	case delay := <-backoff:
		if delay != time.Second {
			t.Fatalf("primeiro backoff=%s", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não entrou em backoff")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run não encerrou durante backoff")
	}
	client.mu.Lock()
	openCalls := client.openCalls
	registrations := []int{client.readyRegistrations, client.interactionRegistrations, client.messageRegistrations}
	client.mu.Unlock()
	if openCalls != 1 || registrations[0] != 1 || registrations[1] != 1 || registrations[2] != 1 {
		t.Fatalf("tentativas=%d handlers=%v", openCalls, registrations)
	}
}

func TestVincularIgnoresOtherMachineAndOnlyOwnersCanRebind(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.MachineName = "PC-TESTE"
	cfg.Discord.BotToken = "fake-token"
	if err := config.Save(home, cfg); err != nil {
		t.Fatal(err)
	}
	client := newFakeAPI()
	client.channels["text"] = &discordgo.Channel{ID: "text", GuildID: "guild", Type: discordgo.ChannelTypeGuildText}
	client.channels["other"] = &discordgo.Channel{ID: "other", GuildID: "guild", Type: discordgo.ChannelTypeGuildText}
	discord := newChannel(home, cfg, client)
	discord.SetLogger(logx.New(filepath.Join(home, "discord.log")))
	bind := func(id, channelID, userID, machine string) {
		discord.handleInteraction(context.Background(), commandInteraction(id, "vincular", channelID, "guild", userID, &discordgo.ApplicationCommandInteractionDataOption{Name: "computador", Value: machine}), func(context.Context, channel.Update) {})
	}
	bind("wrong", "text", "owner", "another-pc")
	if len(client.responses) != 0 {
		t.Fatalf("vínculo para outro computador não foi ignorado: %+v", client.responses)
	}
	bind("first", "text", "owner", "pc-teste")
	loaded, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Discord.ChannelID != "text" || loaded.Discord.GuildID != "guild" || len(loaded.Discord.AllowedUserIDs) != 1 || loaded.Discord.AllowedUserIDs[0] != "owner" {
		t.Fatalf("configuração vinculada=%+v", loaded.Discord)
	}
	if got := client.responses[len(client.responses)-1].response.Data.Content; got != "Computador PC-TESTE vinculado a este canal. As sessões aparecerão em threads aqui." {
		t.Fatalf("confirmação de vínculo=%q", got)
	}
	bind("unauthorized", "other", "stranger", "PC-TESTE")
	loaded, err = config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Discord.ChannelID != "text" || client.responses[len(client.responses)-1].response.Data.Content != "Sem permissão." {
		t.Fatalf("rebind não autorizado: cfg=%+v response=%+v", loaded.Discord, client.responses[len(client.responses)-1])
	}
}

func TestThreadBindingAndUnownedChannelAreRejectedCorrectly(t *testing.T) {
	client := newFakeAPI()
	discord, _ := testBoundChannel(t, client)
	client.channels["thread"] = &discordgo.Channel{ID: "thread", ParentID: "bound", GuildID: "guild", Type: discordgo.ChannelTypeGuildPublicThread}
	discord.handleInteraction(context.Background(), commandInteraction("bind-thread", "vincular", "thread", "guild", "owner", &discordgo.ApplicationCommandInteractionDataOption{Name: "computador", Value: "PC-TESTE"}), func(context.Context, channel.Update) {})
	if got := client.responses[len(client.responses)-1].response.Data.Content; got != "Use /vincular em um canal de texto, não em uma thread." {
		t.Fatalf("resposta em thread=%q", got)
	}
	update := &discordgo.MessageCreate{Message: &discordgo.Message{ID: "1", ChannelID: "other", Author: &discordgo.User{ID: "owner"}, Content: "ignore"}}
	handled := false
	discord.handleMessage(context.Background(), update, func(context.Context, channel.Update) { handled = true })
	if handled {
		t.Fatal("mensagem de outro canal foi roteada")
	}
}

func TestThreadNameRuneLimit(t *testing.T) {
	name := threadName(channel.SessionRef{ID: "id", Name: "nome", Title: strings.Repeat("á", 120)})
	if utf8.RuneCountInString(name) != 100 {
		t.Fatalf("nome de thread tem %d runes", utf8.RuneCountInString(name))
	}
}
