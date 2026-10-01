package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/config"
)

const (
	discordAPIBase                 = "https://discord.com/api/v10"
	nonTerminalDiscordSetupMessage = "setup discord precisa de um terminal interativo para ocultar o token. Execute no PowerShell, Windows Terminal ou Terminal.app."
)

type discordUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type discordApplication struct {
	ID    string `json:"id"`
	Flags int64  `json:"flags"`
}

func Discord(ctx context.Context, home string, cfg config.Config, input *os.File, output io.Writer) (config.Config, error) {
	return discordWithTerminal(ctx, home, cfg, output, newSystemTerminalInput(input), nil, discordAPIBase)
}

func discordWithTerminal(ctx context.Context, home string, cfg config.Config, output io.Writer, terminal terminalInput, client *http.Client, baseURL string) (config.Config, error) {
	if terminal == nil || !terminal.IsTerminal() {
		return cfg, errors.New(nonTerminalDiscordSetupMessage)
	}
	fmt.Fprint(output, "Token do bot do Discord (a entrada não será exibida): ")
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
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if baseURL == "" {
		baseURL = discordAPIBase
	}
	baseURL = strings.TrimRight(baseURL, "/")
	var bot discordUser
	if err := discordGET(ctx, client, baseURL, botToken, "/users/@me", &bot); err != nil {
		return cfg, fmt.Errorf("validar bot do Discord: %w", err)
	}
	var app discordApplication
	if err := discordGET(ctx, client, baseURL, botToken, "/oauth2/applications/@me", &app); err != nil {
		return cfg, fmt.Errorf("validar aplicação do Discord: %w", err)
	}
	cfg.Discord.BotToken = botToken
	if err := config.Save(home, cfg); err != nil {
		return cfg, fmt.Errorf("salvar configuração do Discord: %w", err)
	}
	fmt.Fprintf(output, "Bot validado: %s. Aplicação ID %s.\n", bot.Username, app.ID)
	if app.Flags&(int64(1<<19)|int64(1<<18)) == 0 {
		fmt.Fprintln(output, "Aviso: habilite Message Content Intent no Developer Portal para que mensagens de texto sejam recebidas.")
	}
	fmt.Fprintf(output, "Convide o bot: https://discord.com/oauth2/authorize?client_id=%s&scope=bot%%20applications.commands&permissions=326417583104\n", app.ID)
	fmt.Fprintln(output, "Convide o bot ao servidor, execute agent-bridge daemon stop e, no canal desejado, use /vincular computador:<nome deste computador>.")
	return cfg, nil
}

func discordGET(ctx context.Context, client *http.Client, baseURL, token, path string, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Discord HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(output); err != nil {
		return fmt.Errorf("resposta Discord inválida: %w", err)
	}
	return nil
}
