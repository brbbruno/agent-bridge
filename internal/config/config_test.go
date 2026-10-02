package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDefaultsAndHomeOverride(t *testing.T) {
	cfg := Default()
	if cfg.Port != 47821 || cfg.StopWait != 8*time.Hour || cfg.PermissionWait != 8*time.Hour || cfg.QuestionWait != 8*time.Hour || cfg.StopWaitText != "8h" || cfg.PermissionWaitText != "8h" || cfg.QuestionWaitText != "8h" || !cfg.NotifyWhenPresent {
		t.Fatalf("defaults inesperados: %+v", cfg)
	}
	t.Setenv("AGENT_BRIDGE_HOME", filepath.Join(t.TempDir(), "bridge-home"))
	home, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	if home != filepath.Clean(os.Getenv("AGENT_BRIDGE_HOME")) {
		t.Fatalf("home=%q", home)
	}
}

func TestSaveLoadAndPrivateConfigMode(t *testing.T) {
	home := t.TempDir()
	cfg := Default()
	cfg.Telegram.BotToken = "not-a-real-token"
	cfg.Telegram.ChatID = 55
	cfg.Discord = Discord{BotToken: "not-a-real-discord-token", GuildID: "guild", ChannelID: "channel", AllowedUserIDs: []string{"user"}}
	cfg.StopWaitText = "4s"
	cfg.PermissionWaitText = "5s"
	cfg.QuestionWaitText = "6s"
	if err := Save(home, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Telegram.ChatID != 55 || loaded.Telegram.BotToken != cfg.Telegram.BotToken || loaded.Discord.BotToken != cfg.Discord.BotToken || loaded.Discord.ChannelID != cfg.Discord.ChannelID || len(loaded.Discord.AllowedUserIDs) != 1 || loaded.StopWait != 4*time.Second || loaded.PermissionWait != 5*time.Second || loaded.QuestionWait != 6*time.Second {
		t.Fatalf("configuração carregada incorretamente: %+v", loaded)
	}
	data, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["stop_wait"]; !ok {
		t.Fatal("stop_wait ausente do JSON")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(ConfigPath(home))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode=%o", info.Mode().Perm())
		}
	}
}

func TestMachineNamePrefersConfiguredNickname(t *testing.T) {
	if got := MachineName(Config{MachineName: "  PC-TESTE  "}); got != "PC-TESTE" {
		t.Fatalf("machine name=%q", got)
	}
	if got := MachineName(Config{}); got == "" {
		t.Fatal("hostname fallback está vazio")
	}
}

func TestLoadMissingConfigUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StopWait != 8*time.Hour || cfg.PermissionWait != 8*time.Hour || cfg.QuestionWait != 8*time.Hour {
		t.Fatalf("defaults de espera=%s/%s/%s", cfg.StopWait, cfg.PermissionWait, cfg.QuestionWait)
	}
}

func TestLoadPreservesExplicitWaitAndDefaultsMissingValues(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(ConfigPath(home), []byte(`{"stop_wait":"30m"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StopWait != 30*time.Minute || cfg.PermissionWait != 8*time.Hour || cfg.QuestionWait != 8*time.Hour {
		t.Fatalf("waits after load=%s/%s/%s", cfg.StopWait, cfg.PermissionWait, cfg.QuestionWait)
	}
}

func TestSaveUsesDefaultWaitFallbacks(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Config{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StopWaitText != "8h" || cfg.PermissionWaitText != "8h" || cfg.QuestionWaitText != "8h" {
		t.Fatalf("wait text after save=%q/%q/%q", cfg.StopWaitText, cfg.PermissionWaitText, cfg.QuestionWaitText)
	}
}
