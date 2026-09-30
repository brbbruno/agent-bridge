package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultPort = 47821

type Telegram struct {
	BotToken string `json:"bot_token"`
	ChatID   int64  `json:"chat_id"`
	APIBase  string `json:"api_base"`
}

type Config struct {
	Channel            string        `json:"channel"`
	Telegram           Telegram      `json:"telegram"`
	Port               int           `json:"port"`
	StopWait           time.Duration `json:"-"`
	PermissionWait     time.Duration `json:"-"`
	QuestionWait       time.Duration `json:"-"`
	StopWaitText       string        `json:"stop_wait"`
	PermissionWaitText string        `json:"permission_wait"`
	QuestionWaitText   string        `json:"question_wait"`
	NotifyWhenPresent  bool          `json:"notify_when_present"`
}

func Default() Config {
	return Config{
		Channel:            "telegram",
		Port:               DefaultPort,
		StopWait:           30 * time.Minute,
		PermissionWait:     10 * time.Minute,
		QuestionWait:       30 * time.Minute,
		StopWaitText:       "30m",
		PermissionWaitText: "10m",
		QuestionWaitText:   "30m",
		NotifyWhenPresent:  true,
		Telegram:           Telegram{APIBase: "https://api.telegram.org"},
	}
}

func Home() (string, error) {
	if value := strings.TrimSpace(os.Getenv("AGENT_BRIDGE_HOME")); value != "" {
		return filepath.Clean(value), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "agent-bridge"), nil
}

func EnsureHome(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.Chmod(home, 0o700)
}

func ConfigPath(home string) string { return filepath.Join(home, "config.json") }
func TokenPath(home string) string  { return filepath.Join(home, "daemon.token") }
func StatePath(home string) string  { return filepath.Join(home, "state.json") }
func LogPath(home string) string    { return filepath.Join(home, "agent-bridge.log") }

func Load(home string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("ler config.json: %w", err)
	}
	if cfg.Channel == "" {
		cfg.Channel = "telegram"
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Telegram.APIBase == "" {
		cfg.Telegram.APIBase = "https://api.telegram.org"
	}
	if cfg.StopWaitText == "" {
		cfg.StopWaitText = "30m"
	}
	if cfg.PermissionWaitText == "" {
		cfg.PermissionWaitText = "10m"
	}
	if cfg.QuestionWaitText == "" {
		cfg.QuestionWaitText = "30m"
	}
	if err := cfg.parseDurations(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) parseDurations() error {
	var err error
	if c.StopWait, err = time.ParseDuration(c.StopWaitText); err != nil {
		return fmt.Errorf("stop_wait inválido: %w", err)
	}
	if c.PermissionWait, err = time.ParseDuration(c.PermissionWaitText); err != nil {
		return fmt.Errorf("permission_wait inválido: %w", err)
	}
	if c.QuestionWait, err = time.ParseDuration(c.QuestionWaitText); err != nil {
		return fmt.Errorf("question_wait inválido: %w", err)
	}
	if c.StopWait <= 0 || c.PermissionWait <= 0 || c.QuestionWait <= 0 {
		return errors.New("os tempos de espera devem ser positivos")
	}
	return nil
}

func (c Config) WaitFor(kind string) time.Duration {
	switch kind {
	case "stop":
		return c.StopWait
	case "permission":
		return c.PermissionWait
	case "question":
		return c.QuestionWait
	default:
		return 5 * time.Second
	}
}

func Save(home string, cfg Config) error {
	if err := EnsureHome(home); err != nil {
		return err
	}
	if cfg.Channel == "" {
		cfg.Channel = "telegram"
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.StopWaitText == "" {
		cfg.StopWaitText = "30m"
	}
	if cfg.PermissionWaitText == "" {
		cfg.PermissionWaitText = "10m"
	}
	if cfg.QuestionWaitText == "" {
		cfg.QuestionWaitText = "30m"
	}
	if cfg.Telegram.APIBase == "" {
		cfg.Telegram.APIBase = "https://api.telegram.org"
	}
	if err := cfg.parseDurations(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(ConfigPath(home), append(data, '\n'), 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-bridge-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
