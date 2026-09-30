package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/logx"
)

func EnsureRunning(home string, cfg config.Config, logger *logx.Logger) (*Client, error) {
	token, err := LoadOrCreateToken(home)
	if err != nil {
		return nil, err
	}
	client := NewClient(cfg.Port, token)
	probeCtx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	err = client.Health(probeCtx)
	cancel()
	if err == nil {
		return client, nil
	}

	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("localizar executável do daemon: %w", err)
	}
	env := daemonEnvironment(os.Environ(), home)
	if err := spawnDetached(executable, []string{"daemon"}, env, home); err != nil {
		logger.Errorf("iniciar daemon em segundo plano: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
		err = client.Health(ctx)
		cancel()
		if err == nil {
			return client, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon não respondeu em 5 segundos: %w", err)
}

func daemonEnvironment(environment []string, home string) []string {
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		if !strings.HasPrefix(strings.ToUpper(item), "ACP_BACKEND=") {
			filtered = append(filtered, item)
		}
	}
	return replaceEnv(filtered, "AGENT_BRIDGE_HOME", home)
}

func replaceEnv(environment []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	result := make([]string, 0, len(environment)+1)
	found := false
	for _, item := range environment {
		if strings.HasPrefix(strings.ToUpper(item), prefix) {
			if !found {
				result = append(result, key+"="+value)
				found = true
			}
			continue
		}
		result = append(result, item)
	}
	if !found {
		result = append(result, key+"="+value)
	}
	return result
}
