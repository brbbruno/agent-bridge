package hook

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/daemon"
	"github.com/brbbruno/agent-bridge/internal/logx"
	"github.com/brbbruno/agent-bridge/internal/model"
)

func Execute(kind model.EventType, agent model.Agent, stdin []byte, env map[string]string) (stdout []byte) {
	home, err := config.Home()
	if err != nil {
		return nil
	}
	logger := logx.New(config.LogPath(home))
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Errorf("panic no hook: %v", recovered)
			stdout = nil
		}
	}()
	if agent == model.AgentClaude && IsClaudeUnderDevin(env) {
		logger.Infof("hook claude ignorado sob Devin; DEVIN_PROJECT_DIR presente, CLAUDE_PROJECT_DIR presente=%t", env["CLAUDE_PROJECT_DIR"] != "")
		return nil
	}
	event, err := Parse(kind, agent, env, stdin)
	if err != nil {
		logger.Errorf("interpretar payload do hook %s/%s: %v", agent, kind, err)
		return nil
	}
	cfg, err := config.Load(home)
	if err != nil {
		logger.Errorf("ler configuração do hook: %v", err)
		return nil
	}
	if agent == model.AgentDevin && (kind == model.EventStop || kind == model.EventPermission || kind == model.EventQuestion) && event.CWD != "" {
		if exe := devinExecutable(cfg); exe != "" {
			titleCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			event.SessionTitle = truncate(lookupDevinTitle(titleCtx, exe, event.CWD, event.SessionID), 80)
			cancel()
		}
	}
	client, err := daemon.EnsureRunning(home, cfg, logger)
	if err != nil {
		logger.Errorf("garantir daemon ativo: %v", err)
		return nil
	}
	wait := 12 * time.Second
	if kind == model.EventProgress {
		wait = 3 * time.Second
	} else if kind == model.EventStop || kind == model.EventPermission || kind == model.EventQuestion {
		wait = cfg.WaitFor(string(kind)) + 15*time.Second
		if wait < 20*time.Second {
			wait = 20 * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	resolution, err := client.Handle(ctx, event)
	if err != nil {
		logger.Errorf("daemon indisponível para %s/%s na sessão %s: %v", agent, kind, event.SessionID, err)
		return nil
	}
	stdout, err = Encode(agent, kind, resolution)
	if err != nil {
		logger.Errorf("formatar resposta do hook %s/%s: %v", agent, kind, err)
		return nil
	}
	return stdout
}

func Environment() map[string]string {
	values := map[string]string{}
	for _, item := range os.Environ() {
		for index := 0; index < len(item); index++ {
			if item[index] == '=' {
				values[item[:index]] = item[index+1:]
				break
			}
		}
	}
	return values
}

func ParseAgent(value string) (model.Agent, error) {
	agent := model.Agent(value)
	if agent != model.AgentDevin && agent != model.AgentClaude {
		return "", fmt.Errorf("agente inválido: %s", value)
	}
	return agent, nil
}
