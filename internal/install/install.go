package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/config"
)

type Options struct {
	Agent         string
	Scope         string
	ProjectDir    string
	Executable    string
	UserConfigDir string
	HomeDir       string
	WaitConfig    config.Config
}

type Result struct {
	Path     string
	Backup   string
	Warnings []string
}

type hookEntry struct {
	Event   string
	Matcher string
	Timeout int
	Kind    string
	Command string
}

func Install(options Options) (Result, error)   { return mutate(options, true) }
func Uninstall(options Options) (Result, error) { return mutate(options, false) }

func Entries(agent string) ([]string, error) {
	switch agent {
	case "devin":
		return []string{"Stop", "PermissionRequest", "PreToolUse", "UserPromptSubmit", "SessionEnd", "PostToolUse"}, nil
	case "claude":
		return []string{"Stop", "PermissionRequest", "PreToolUse", "UserPromptSubmit", "SessionEnd", "PostToolUse"}, nil
	default:
		return nil, fmt.Errorf("agente inválido: %s", agent)
	}
}

func mutate(options Options, adding bool) (Result, error) {
	if options.Agent != "devin" && options.Agent != "claude" {
		return Result{}, errors.New("--agent deve ser devin ou claude")
	}
	if options.Scope != "user" && options.Scope != "project" {
		return Result{}, errors.New("--scope deve ser user ou project")
	}
	path, wholeFile, err := targetPath(options)
	if err != nil {
		return Result{}, err
	}
	if options.Executable == "" {
		options.Executable, err = os.Executable()
		if err != nil {
			return Result{}, err
		}
	}
	warnings := []string{}
	if IsTransientExecutable(options.Executable) {
		warnings = append(warnings, "O executável está em uma pasta temporária ou Downloads; mova-o para um local permanente e reinstale os hooks.")
	}

	original, readErr := os.ReadFile(path)
	existed := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return Result{}, readErr
	}
	root := map[string]any{}
	if existed && len(bytes.TrimSpace(original)) > 0 {
		if err := json.Unmarshal(original, &root); err != nil {
			return Result{}, fmt.Errorf("JSON inválido em %s: %w", path, err)
		}
	}
	if root == nil {
		root = map[string]any{}
	}
	hooks, err := hooksObject(root, wholeFile)
	if err != nil {
		return Result{}, err
	}
	entries, _ := Entries(options.Agent)
	if !adding && !hasOwned(hooks, entries) {
		return Result{Path: path, Warnings: warnings}, nil
	}
	for _, event := range entries {
		if err := removeOwned(hooks, event); err != nil {
			return Result{}, err
		}
	}
	if adding {
		waits := waitTimeouts(options.WaitConfig)
		for _, entry := range makeEntries(options.Agent, waits) {
			entry.Command, err = hookCommand(options.Executable, entry.Kind, options.Agent)
			if err != nil {
				return Result{}, err
			}
			if err := addEntry(hooks, entry); err != nil {
				return Result{}, err
			}
		}
	}
	if !wholeFile && isEmpty(hooks) {
		delete(root, "hooks")
	}
	if !adding && isEmpty(root) {
		if !existed {
			return Result{Path: path, Warnings: warnings}, nil
		}
		backup, err := makeBackup(path, original, fileMode(path))
		if err != nil {
			return Result{}, err
		}
		if err := os.Remove(path); err != nil {
			return Result{}, err
		}
		return Result{Path: path, Backup: backup, Warnings: warnings}, nil
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return Result{}, err
	}
	data = append(data, '\n')
	if existed && bytes.Equal(original, data) {
		return Result{Path: path, Warnings: warnings}, nil
	}
	mode := os.FileMode(0o644)
	if existed {
		mode = fileMode(path)
	}
	backup := ""
	if existed {
		backup, err = makeBackup(path, original, mode)
		if err != nil {
			return Result{}, err
		}
	}
	if err := writeAtomic(path, data, mode); err != nil {
		return Result{}, err
	}
	return Result{Path: path, Backup: backup, Warnings: warnings}, nil
}

func makeEntries(agent string, waits map[string]int) []hookEntry {
	questionMatcher := "^ask_user_question$"
	if agent == "claude" {
		questionMatcher = "^AskUserQuestion$"
	}
	return []hookEntry{
		{Event: "Stop", Matcher: "", Timeout: waits["Stop"], Kind: "stop"},
		{Event: "PermissionRequest", Matcher: "", Timeout: waits["PermissionRequest"], Kind: "permission"},
		{Event: "PreToolUse", Matcher: questionMatcher, Timeout: waits["PreToolUse"], Kind: "question"},
		{Event: "UserPromptSubmit", Matcher: "", Timeout: waits["UserPromptSubmit"], Kind: "prompt"},
		{Event: "SessionEnd", Matcher: "", Timeout: waits["SessionEnd"], Kind: "session-end"},
		{Event: "PostToolUse", Matcher: "", Timeout: 10, Kind: "progress"},
	}
}

func waitTimeouts(cfg config.Config) map[string]int {
	defaults := config.Default()
	if cfg.StopWait <= 0 {
		cfg.StopWait = defaults.StopWait
	}
	if cfg.PermissionWait <= 0 {
		cfg.PermissionWait = defaults.PermissionWait
	}
	if cfg.QuestionWait <= 0 {
		cfg.QuestionWait = defaults.QuestionWait
	}
	seconds := func(duration time.Duration) int { return int((duration+time.Second-1)/time.Second) + 120 }
	return map[string]int{
		"Stop":              seconds(cfg.StopWait),
		"PermissionRequest": seconds(cfg.PermissionWait),
		"PreToolUse":        seconds(cfg.QuestionWait),
		"UserPromptSubmit":  120,
		"SessionEnd":        120,
	}
}

func addEntry(hooks map[string]any, entry hookEntry) error {
	groups := []any{}
	if value, exists := hooks[entry.Event]; exists {
		var ok bool
		groups, ok = value.([]any)
		if !ok {
			return fmt.Errorf("evento %s existente não é um array JSON", entry.Event)
		}
	}
	hook := map[string]any{"type": "command", "command": entry.Command, "timeout": entry.Timeout}
	group := map[string]any{"matcher": entry.Matcher, "hooks": []any{hook}}
	groups = append(groups, group)
	hooks[entry.Event] = groups
	return nil
}

func hasOwned(hooks map[string]any, events []string) bool {
	for _, event := range events {
		groups, _ := hooks[event].([]any)
		for _, groupValue := range groups {
			group, _ := groupValue.(map[string]any)
			items, _ := group["hooks"].([]any)
			for _, item := range items {
				hook, _ := item.(map[string]any)
				if ownedCommand(stringValue(hook["command"])) {
					return true
				}
			}
		}
	}
	return false
}

func removeOwned(hooks map[string]any, event string) error {
	value, exists := hooks[event]
	if !exists {
		return nil
	}
	groups, ok := value.([]any)
	if !ok {
		return fmt.Errorf("evento %s existente não é um array JSON", event)
	}
	keptGroups := make([]any, 0, len(groups))
	for _, value := range groups {
		group, ok := value.(map[string]any)
		if !ok {
			keptGroups = append(keptGroups, value)
			continue
		}
		hooksList, ok := group["hooks"].([]any)
		if !ok {
			keptGroups = append(keptGroups, value)
			continue
		}
		keptHooks := make([]any, 0, len(hooksList))
		removed := false
		for _, hookValue := range hooksList {
			hook, ok := hookValue.(map[string]any)
			if ok && ownedCommand(stringValue(hook["command"])) {
				removed = true
				continue
			}
			keptHooks = append(keptHooks, hookValue)
		}
		if !removed {
			keptGroups = append(keptGroups, value)
			continue
		}
		if len(keptHooks) > 0 {
			group["hooks"] = keptHooks
			keptGroups = append(keptGroups, group)
		}
	}
	if len(keptGroups) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = keptGroups
	}
	return nil
}

func ownedCommand(command string) bool {
	command = strings.ToLower(command)
	return strings.Contains(command, "agent-bridge") && strings.Contains(command, " hook ")
}

func targetPath(options Options) (string, bool, error) {
	if options.Scope == "project" {
		project := options.ProjectDir
		if project == "" {
			var err error
			project, err = os.Getwd()
			if err != nil {
				return "", false, err
			}
		}
		if options.Agent == "devin" {
			return filepath.Join(project, ".devin", "hooks.v1.json"), true, nil
		}
		return filepath.Join(project, ".claude", "settings.json"), false, nil
	}
	if options.UserConfigDir == "" {
		var err error
		options.UserConfigDir, err = os.UserConfigDir()
		if err != nil {
			return "", false, err
		}
	}
	if options.Agent == "devin" {
		return filepath.Join(options.UserConfigDir, "devin", "config.json"), false, nil
	}
	home := options.HomeDir
	if home == "" {
		current, err := user.Current()
		if err != nil {
			return "", false, err
		}
		home = current.HomeDir
	}
	return filepath.Join(home, ".claude", "settings.json"), false, nil
}

var safeWindowsExecutablePath = regexp.MustCompile(`^[A-Za-z]:/[A-Za-z0-9._/-]+$`)

func hookCommand(executable, kind, agent string) (string, error) {
	path, err := filepath.Abs(executable)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, `\`, "/")
		if safeWindowsExecutablePath.MatchString(path) {
			return path + ` hook ` + kind + ` --agent ` + strings.ToLower(agent), nil
		}
		return `"` + strings.ReplaceAll(path, `"`, `\"`) + `" hook ` + kind + ` --agent ` + strings.ToLower(agent), nil
	}
	return shellQuote(path) + " hook " + kind + " --agent " + strings.ToLower(agent), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func IsTransientExecutable(path string) bool {
	clean := strings.ToLower(filepath.Clean(path))
	temp := strings.ToLower(os.TempDir())
	home, _ := os.UserHomeDir()
	downloads := strings.ToLower(filepath.Join(home, "Downloads"))
	return strings.HasPrefix(clean, temp+string(os.PathSeparator)) || strings.HasPrefix(clean, downloads+string(os.PathSeparator))
}

func hooksObject(root map[string]any, wholeFile bool) (map[string]any, error) {
	if wholeFile {
		return root, nil
	}
	if current, exists := root["hooks"]; exists {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, errors.New("a chave hooks existente não é um objeto JSON")
		}
		return object, nil
	}
	object := map[string]any{}
	root["hooks"] = object
	return object, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func isEmpty(value map[string]any) bool { return len(value) == 0 }

func fileMode(path string) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return 0o644
	}
	return info.Mode().Perm()
}

func makeBackup(path string, data []byte, mode os.FileMode) (string, error) {
	stamp := time.Now().Format("20060102-150405.000000000")
	backup := path + ".bak." + stamp
	if err := os.WriteFile(backup, data, mode); err != nil {
		return "", err
	}
	return backup, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-bridge-*.tmp")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
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
	return os.Rename(temporary, path)
}
