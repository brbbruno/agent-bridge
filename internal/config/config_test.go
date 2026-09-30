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
	if cfg.Port != 47821 || cfg.StopWait != 30*time.Minute || cfg.PermissionWait != 10*time.Minute || cfg.QuestionWait != 30*time.Minute || !cfg.NotifyWhenPresent {
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
	if loaded.Telegram.ChatID != 55 || loaded.Telegram.BotToken != cfg.Telegram.BotToken || loaded.StopWait != 4*time.Second || loaded.PermissionWait != 5*time.Second || loaded.QuestionWait != 6*time.Second {
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

func TestLoadMissingConfigUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StopWait != 30*time.Minute {
		t.Fatalf("StopWait=%s", cfg.StopWait)
	}
}
