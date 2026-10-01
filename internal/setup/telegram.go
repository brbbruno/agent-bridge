package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/brbbruno/agent-bridge/internal/channel/telegram"
	"github.com/brbbruno/agent-bridge/internal/config"
	"golang.org/x/term"
)

const nonTerminalSetupMessage = "setup telegram precisa de um terminal interativo para ocultar o token. Execute no PowerShell, Windows Terminal ou Terminal.app; o Git Bash com mintty não permite ocultar a entrada. No Git Bash, tente: winpty agent-bridge setup telegram"

type terminalInput interface {
	IsTerminal() bool
	ReadPassword() ([]byte, error)
	ReadLine() (string, error)
}

type systemTerminalInput struct {
	file   *os.File
	reader *bufio.Reader
}

func newSystemTerminalInput(file *os.File) *systemTerminalInput {
	return &systemTerminalInput{file: file, reader: bufio.NewReader(file)}
}

func (input *systemTerminalInput) IsTerminal() bool {
	return input.file != nil && term.IsTerminal(int(input.file.Fd()))
}

func (input *systemTerminalInput) ReadPassword() ([]byte, error) {
	if input.file == nil {
		return nil, errors.New("terminal indisponível")
	}
	return term.ReadPassword(int(input.file.Fd()))
}

func (input *systemTerminalInput) ReadLine() (string, error) {
	line, err := input.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return line, nil
}

func Telegram(ctx context.Context, home string, cfg config.Config, input *os.File, output io.Writer) (config.Config, error) {
	return telegramWithTerminal(ctx, home, cfg, output, newSystemTerminalInput(input))
}

func telegramWithTerminal(ctx context.Context, home string, cfg config.Config, output io.Writer, terminal terminalInput) (config.Config, error) {
	if terminal == nil || !terminal.IsTerminal() {
		return cfg, errors.New(nonTerminalSetupMessage)
	}
	fmt.Fprint(output, "Token do bot do Telegram (a entrada não será exibida): ")
	token, err := terminal.ReadPassword()
	fmt.Fprintln(output)
	if err != nil {
		return cfg, err
	}
	botToken := strings.TrimSpace(string(token))
	for i := range token {
		token[i] = 0
	}
	if botToken == "" {
		return cfg, errors.New("token vazio")
	}
	apiBase := cfg.Telegram.APIBase
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	bot := telegram.New(botToken, 0, apiBase)
	meCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	me, err := bot.GetMe(meCtx)
	cancel()
	if err != nil {
		return cfg, fmt.Errorf("validar token do Telegram: %w", err)
	}
	fmt.Fprintf(output, "Bot validado: @%s. Envie /start para esse bot em uma conversa privada; aguardando por até 2 minutos...\n", me.Username)

	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var chatID int64
	var offset int64
	for waitCtx.Err() == nil {
		pollCtx, pollCancel := context.WithTimeout(waitCtx, 35*time.Second)
		updates, pollErr := bot.Updates(pollCtx, offset, 30)
		pollCancel()
		if pollErr != nil {
			if waitCtx.Err() != nil {
				break
			}
			continue
		}
		for _, update := range updates {
			if update.ID >= offset {
				offset = update.ID + 1
			}
			if !update.Private || update.ChatID == 0 || !isStartCommand(update.Text) {
				continue
			}
			fmt.Fprintf(output, "Recebi /start de %s; chat_id=%d.\nConfirmar este chat? [s/N] ", senderName(update.FirstName, update.Username), update.ChatID)
			answer, err := terminal.ReadLine()
			if err != nil {
				return cfg, fmt.Errorf("ler confirmação do chat: %w", err)
			}
			if !strings.EqualFold(strings.TrimSpace(answer), "s") {
				fmt.Fprintln(output, "Chat não confirmado; aguardando outro /start.")
				continue
			}
			chatID = update.ChatID
			break
		}
		if chatID != 0 {
			break
		}
	}
	if chatID == 0 {
		return cfg, errors.New("nenhuma conversa privada /start confirmada em 2 minutos")
	}
	cfg.Channel = "telegram"
	cfg.Telegram = config.Telegram{BotToken: botToken, ChatID: chatID, APIBase: apiBase}
	if err := config.Save(home, cfg); err != nil {
		return cfg, fmt.Errorf("salvar configuração: %w", err)
	}
	bot = telegram.New(botToken, chatID, apiBase)
	confirmCtx, confirmCancel := context.WithTimeout(ctx, 10*time.Second)
	defer confirmCancel()
	if _, err := bot.Send(confirmCtx, channel.Outgoing{Text: "Configuração do agent-bridge concluída. Use /help para ver os comandos."}); err != nil {
		return cfg, fmt.Errorf("configuração salva, mas a confirmação não foi enviada: %w", err)
	}
	return cfg, nil
}

func isStartCommand(text string) bool {
	fields := strings.Fields(text)
	return len(fields) > 0 && fields[0] == "/start"
}

func senderName(firstName, username string) string {
	firstName = strings.TrimSpace(firstName)
	username = strings.TrimSpace(username)
	if firstName == "" {
		firstName = "usuário"
	}
	if username != "" {
		return firstName + " (@" + username + ")"
	}
	return firstName
}
