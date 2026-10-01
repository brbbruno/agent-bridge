package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/channel"
	discordchannel "github.com/brbbruno/agent-bridge/internal/channel/discord"
	"github.com/brbbruno/agent-bridge/internal/channel/telegram"
	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/daemon"
	"github.com/brbbruno/agent-bridge/internal/hook"
	"github.com/brbbruno/agent-bridge/internal/install"
	"github.com/brbbruno/agent-bridge/internal/logx"
	"github.com/brbbruno/agent-bridge/internal/model"
	"github.com/brbbruno/agent-bridge/internal/setup"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return 0
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(os.Stdout, "agent-bridge %s (%s, %s)\n", version, commit, date)
		return 0
	case "help", "--help", "-h":
		printHelp(os.Stdout)
		return 0
	case "hook":
		return runHook(args[1:])
	case "daemon":
		return runDaemonCommand(args[1:])
	case "away":
		return runAway(args[1:])
	case "test":
		return runTest()
	case "setup":
		return runSetup(args[1:])
	case "install":
		return runInstall(args[1:], true)
	case "uninstall":
		return runInstall(args[1:], false)
	default:
		fmt.Fprintf(os.Stderr, "Comando desconhecido: %s\n\n", args[0])
		printHelp(os.Stderr)
		return 2
	}
}

func runHook(args []string) int {
	if len(args) == 0 {
		logHookError(errors.New("subcomando hook ausente"))
		return 0
	}
	kind := model.EventType(args[0])
	valid := map[model.EventType]bool{
		model.EventStop: true, model.EventPermission: true, model.EventQuestion: true,
		model.EventPrompt: true, model.EventSessionEnd: true, model.EventProgress: true,
	}
	if !valid[kind] {
		logHookError(fmt.Errorf("tipo de hook desconhecido: %s", kind))
		return 0
	}
	agentName := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--agent" && i+1 < len(args) {
			i++
			agentName = args[i]
			break
		}
	}
	agent, err := hook.ParseAgent(agentName)
	if err != nil {
		logHookError(err)
		return 0
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		logHookError(err)
		return 0
	}
	output := hook.Execute(kind, agent, data, hook.Environment())
	if len(output) > 0 {
		_, _ = os.Stdout.Write(output)
	}
	return 0
}

func logHookError(err error) {
	home, homeErr := config.Home()
	if homeErr == nil {
		logx.New(config.LogPath(home)).Errorf("hook fail-open: %v", err)
	}
}

func runDaemonCommand(args []string) int {
	if len(args) == 0 {
		if err := runDaemonForeground(); err != nil {
			fmt.Fprintln(os.Stderr, "Erro no daemon:", err)
			return 1
		}
		return 0
	}
	home, cfg, logger, err := loadRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		return 1
	}
	switch args[0] {
	case "start":
		wasRunning := daemonHealth(home, cfg)
		client, err := daemon.EnsureRunning(home, cfg, logger)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Não foi possível iniciar o daemon:", err)
			return 1
		}
		if wasRunning {
			fmt.Fprintln(os.Stdout, "Daemon já estava ativo.")
		} else {
			fmt.Fprintln(os.Stdout, "Daemon iniciado.")
		}
		_ = client
		return 0
	case "stop":
		client, err := existingClient(home, cfg)
		if err != nil {
			fmt.Fprintln(os.Stdout, "Daemon não está ativo.")
			return 0
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Stop(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "Não foi possível parar o daemon:", err)
			return 1
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			checkCtx, checkCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			healthErr := client.Health(checkCtx)
			checkCancel()
			if healthErr != nil {
				fmt.Fprintln(os.Stdout, "Daemon parado.")
				return 0
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprintln(os.Stderr, "Daemon continuou ativo após o pedido de parada.")
		return 1
	case "status":
		client, err := existingClient(home, cfg)
		if err != nil {
			fmt.Fprintln(os.Stdout, "Daemon inativo.")
			return 0
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		status, err := client.GetStatus(ctx)
		if err != nil {
			fmt.Fprintln(os.Stdout, "Daemon inativo.")
			return 0
		}
		printStatus(status)
		return 0
	default:
		fmt.Fprintln(os.Stderr, "Uso: agent-bridge daemon [start|stop|status]")
		return 2
	}
}

func runDaemonForeground() error {
	home, err := config.Home()
	if err != nil {
		return err
	}
	if err := config.EnsureHome(home); err != nil {
		return err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return err
	}
	token, err := daemon.LoadOrCreateToken(home)
	if err != nil {
		return err
	}
	logger := logx.New(config.LogPath(home))
	var channels []channel.Channel
	if bot := makeTelegramChannel(cfg); bot != nil {
		bot.SetLogger(logger)
		channels = append(channels, bot)
	}
	if cfg.Discord.BotToken != "" {
		discord := discordchannel.New(home, cfg)
		discord.SetLogger(logger)
		channels = append(channels, discord)
	}
	server, err := daemon.NewServer(home, cfg, token, channels, logger)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	return server.Run(ctx)
}

func makeTelegramChannel(cfg config.Config) *telegram.Client {
	if cfg.Telegram.BotToken == "" || cfg.Telegram.ChatID == 0 {
		return nil
	}
	return telegram.New(cfg.Telegram.BotToken, cfg.Telegram.ChatID, cfg.Telegram.APIBase)
}

func runAway(args []string) int {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off" && args[0] != "status") {
		fmt.Fprintln(os.Stderr, "Uso: agent-bridge away on|off|status")
		return 2
	}
	home, cfg, logger, err := loadRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		return 1
	}
	client, err := daemon.EnsureRunning(home, cfg, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Não foi possível conectar ao daemon:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	switch args[0] {
	case "on":
		if err := client.SetAway(ctx, true); err != nil {
			fmt.Fprintln(os.Stderr, "Erro:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "Modo ausente ativado.")
	case "off":
		if err := client.SetAway(ctx, false); err != nil {
			fmt.Fprintln(os.Stderr, "Erro:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "Modo ausente desativado.")
	case "status":
		status, err := client.GetStatus(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Erro:", err)
			return 1
		}
		printStatus(status)
	}
	return 0
}

func runTest() int {
	home, cfg, logger, err := loadRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		return 1
	}
	client, err := daemon.EnsureRunning(home, cfg, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Não foi possível conectar ao daemon:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Test(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Falha ao enviar teste:", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "Notificação de teste enviada.")
	return 0
}

func runSetup(args []string) int {
	if len(args) != 1 || (args[0] != "telegram" && args[0] != "discord") {
		fmt.Fprintln(os.Stderr, "Uso: agent-bridge setup telegram|discord")
		return 2
	}
	home, cfg, _, err := loadRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		return 1
	}
	if err := stopExistingDaemon(home, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Não é possível configurar o %s: %v\n", args[0], err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute+20*time.Second)
	defer cancel()
	if args[0] == "telegram" {
		if _, err := setup.Telegram(ctx, home, cfg, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "Falha na configuração do Telegram:", err)
			return 1
		}
	} else if _, err := setup.Discord(ctx, home, cfg, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Falha na configuração do Discord:", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "Configuração concluída.")
	return 0
}

func stopExistingDaemon(home string, cfg config.Config) error {
	token, err := os.ReadFile(config.TokenPath(home))
	if err == nil {
		client := daemon.NewClient(cfg.Port, strings.TrimSpace(string(token)))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		healthErr := client.Health(ctx)
		cancel()
		if healthErr == nil {
			ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			stopErr := client.Stop(ctx)
			cancel()
			if stopErr != nil {
				return fmt.Errorf("daemon ativo e não pôde ser parado: %w", stopErr)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
				err = client.Health(ctx)
				cancel()
				if err != nil {
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return errors.New("daemon continuou ativo após pedido de parada")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	connection, dialErr := net.DialTimeout("tcp", "127.0.0.1:"+fmt.Sprint(cfg.Port), 200*time.Millisecond)
	if dialErr == nil {
		connection.Close()
		return errors.New("a porta do daemon está ocupada por um processo que não pôde ser autenticado; pare-o manualmente")
	}
	return nil
}

func runInstall(args []string, adding bool) int {
	name := "uninstall"
	if adding {
		name = "install"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	agent := flags.String("agent", "", "")
	scope := flags.String("scope", "", "")
	project := flags.String("project-dir", "", "")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Argumentos inválidos: %v\n", err)
		return 2
	}
	_, cfg, _, err := loadRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		return 1
	}
	userConfig, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		return 1
	}
	operation := install.Uninstall
	if adding {
		operation = install.Install
	}
	result, err := operation(install.Options{Agent: *agent, Scope: *scope, ProjectDir: *project, UserConfigDir: userConfig, WaitConfig: cfg})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		return 1
	}
	verb := "removidos"
	if adding {
		verb = "instalados"
	}
	fmt.Fprintf(os.Stdout, "Hooks %s em %s.\n", verb, result.Path)
	if result.Backup != "" {
		fmt.Fprintf(os.Stdout, "Backup: %s\n", result.Backup)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stdout, "Aviso: %s\n", warning)
	}
	return 0
}

func loadRuntime() (string, config.Config, *logx.Logger, error) {
	home, err := config.Home()
	if err != nil {
		return "", config.Config{}, nil, err
	}
	if err := config.EnsureHome(home); err != nil {
		return "", config.Config{}, nil, err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return "", config.Config{}, nil, err
	}
	return home, cfg, logx.New(config.LogPath(home)), nil
}

func existingClient(home string, cfg config.Config) (*daemon.Client, error) {
	token, err := os.ReadFile(config.TokenPath(home))
	if err != nil {
		return nil, err
	}
	client := daemon.NewClient(cfg.Port, strings.TrimSpace(string(token)))
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := client.Health(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

func daemonHealth(home string, cfg config.Config) bool {
	_, err := existingClient(home, cfg)
	return err == nil
}

func printStatus(status daemon.Status) {
	mode := "no computador"
	if status.Away {
		mode = "ausente"
	}
	fmt.Fprintf(os.Stdout, "Modo: %s\nDaemon ativo há: %s\nSessões aguardando: %d\n", mode, status.Uptime.Round(time.Second), status.Waiting)
}

func printHelp(output io.Writer) {
	fmt.Fprintln(output, `agent-bridge — ponte de hooks do agente para Telegram e Discord

Uso:
  agent-bridge setup telegram|discord
  agent-bridge install --agent devin|claude --scope user|project [--project-dir DIR]
  agent-bridge uninstall --agent devin|claude --scope user|project [--project-dir DIR]
  agent-bridge away on|off|status
  agent-bridge test
  agent-bridge daemon [start|stop|status]
  agent-bridge version

Hooks internos (invocados automaticamente):
  agent-bridge hook <stop|permission|question|prompt|session-end|progress> --agent devin|claude`)
}
