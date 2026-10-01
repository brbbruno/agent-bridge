package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMergeIdempotencyBackupAndUninstallFourTargets(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		scope string
	}{
		{"Devin user", "devin", "user"},
		{"Devin project", "devin", "project"},
		{"Claude user", "claude", "user"},
		{"Claude project", "claude", "project"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			userConfig := filepath.Join(base, "user-config")
			home := filepath.Join(base, "home")
			project := filepath.Join(base, "repo with spaces")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}
			path := expectedPath(test.agent, test.scope, userConfig, home, project)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			original := []byte(`{"other":{"kept":true},"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"echo keep-me"}]}]}}` + "\n")
			if test.agent == "devin" && test.scope == "project" {
				original = []byte(`{"Other":{"kept":true},"Stop":[{"matcher":"","hooks":[{"type":"command","command":"echo keep-me"}]}]}` + "\n")
			}
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			options := Options{Agent: test.agent, Scope: test.scope, ProjectDir: project, Executable: filepath.Join(base, "Program Files", "Agent Bridge", "agent-bridge.exe"), UserConfigDir: userConfig, HomeDir: home}
			first, err := Install(options)
			if err != nil {
				t.Fatal(err)
			}
			if first.Backup == "" {
				t.Fatal("instalação existente não criou backup")
			}
			backup, err := os.ReadFile(first.Backup)
			if err != nil || string(backup) != string(original) {
				t.Fatalf("backup não preservou original: err=%v", err)
			}
			installed, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			count, own := countOwned(t, installed)
			if count != 6 {
				t.Fatalf("hooks próprios=%d; esperava 6", count)
			}
			if !strings.Contains(own, " hook stop --agent "+test.agent) {
				t.Fatalf("comando sem subcomando/agent: %s", own)
			}
			questionMatcher := "^ask_user_question$"
			if test.agent == "claude" {
				questionMatcher = "^AskUserQuestion$"
			}
			if !strings.Contains(own, `"matcher": "`+questionMatcher+`"`) {
				t.Fatalf("matcher de pergunta incorreto: %s", own)
			}
			if !strings.Contains(own, `"timeout": 1920`) || !strings.Contains(own, `"timeout": 720`) || !strings.Contains(own, `"timeout": 10`) || !strings.Contains(own, `"PostToolUse"`) {
				t.Fatalf("timeouts ou evento PostToolUse incorretos: %s", own)
			}
			if runtime.GOOS == "windows" && !strings.Contains(own, "C:/") {
				t.Fatalf("comando Windows deve usar barras: %s", own)
			}
			second, err := Install(options)
			if err != nil {
				t.Fatal(err)
			}
			if second.Backup != "" {
				t.Fatal("instalação idempotente criou backup desnecessário")
			}
			installedAgain, _ := os.ReadFile(path)
			count, _ = countOwned(t, installedAgain)
			if count != 6 {
				t.Fatalf("instalação repetida duplicou hooks: %d", count)
			}
			removed, err := Uninstall(options)
			if err != nil {
				t.Fatal(err)
			}
			if removed.Backup == "" {
				t.Fatal("uninstall existente não criou backup")
			}
			final, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			count, finalText := countOwned(t, final)
			if count != 0 || !strings.Contains(finalText, "keep-me") || !strings.Contains(finalText, "kept") {
				t.Fatalf("uninstall não preservou configurações alheias: %s", finalText)
			}
			secondUninstall, err := Uninstall(options)
			if err != nil || secondUninstall.Backup != "" {
				t.Fatalf("uninstall repetido deveria ser idempotente: %+v, err=%v", secondUninstall, err)
			}
		})
	}
}

func TestUninstallRemovesFileCreatedByInstall(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	options := Options{Agent: "devin", Scope: "project", ProjectDir: project, Executable: filepath.Join(base, "agent-bridge")}
	result, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
		t.Fatalf("arquivo criado pelo install deveria ser removido, stat err=%v", err)
	}
}

func TestWindowsHookCommandQuotesOnlyUnsafePaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("a sintaxe é específica do Windows")
	}
	safe, err := hookCommand(`C:\agent\agent-bridge.exe`, "stop", "devin")
	if err != nil {
		t.Fatal(err)
	}
	if safe != `C:/agent/agent-bridge.exe hook stop --agent devin` {
		t.Fatalf("caminho simples deveria ficar sem aspas: %q", safe)
	}
	unsafe, err := hookCommand(`C:\Program Files\agent-bridge.exe`, "stop", "devin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(unsafe, `"C:/Program Files/agent-bridge.exe"`) {
		t.Fatalf("caminho com espaços deveria ser citado: %q", unsafe)
	}
}

func expectedPath(agent, scope, userConfig, home, project string) string {
	if scope == "project" {
		if agent == "devin" {
			return filepath.Join(project, ".devin", "hooks.v1.json")
		}
		return filepath.Join(project, ".claude", "settings.json")
	}
	if agent == "devin" {
		return filepath.Join(userConfig, "devin", "config.json")
	}
	return filepath.Join(home, ".claude", "settings.json")
}

func countOwned(t *testing.T, data []byte) (int, string) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("JSON inválido: %v", err)
	}
	var hooks map[string]any
	if candidate, ok := root["hooks"].(map[string]any); ok {
		hooks = candidate
	} else {
		hooks = root
	}
	count := 0
	for _, value := range hooks {
		groups, _ := value.([]any)
		for _, groupValue := range groups {
			group, _ := groupValue.(map[string]any)
			items, _ := group["hooks"].([]any)
			for _, item := range items {
				hook, _ := item.(map[string]any)
				command, _ := hook["command"].(string)
				if ownedCommand(command) {
					count++
				}
			}
		}
	}
	return count, string(data)
}
