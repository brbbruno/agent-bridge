package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/brbbruno/agent-bridge/internal/config"
)

func parseDevinListTitle(data []byte, sessionID string) string {
	var sessions []struct {
		ID      string `json:"id"`
		ShortID string `json:"short_id"`
		Title   string `json:"title"`
	}
	if err := json.Unmarshal(data, &sessions); err != nil {
		return ""
	}
	for _, session := range sessions {
		if session.ID != sessionID && session.ShortID != sessionID {
			continue
		}
		title := strings.TrimSpace(session.Title)
		if strings.EqualFold(title, "Untitled") {
			return ""
		}
		return title
	}
	return ""
}

func devinExecutable(cfg config.Config) string {
	if exe := strings.TrimSpace(cfg.DevinExe); exe != "" {
		return exe
	}
	if exe, err := exec.LookPath("devin"); err == nil {
		return exe
	}
	if runtime.GOOS != "windows" {
		return ""
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return ""
	}
	exe := filepath.Join(localAppData, "Programs", "Devin", "resources", "app", "extensions", "windsurf", "devin", "bin", "devin.exe")
	if _, err := os.Stat(exe); err != nil {
		return ""
	}
	return exe
}

var lookupDevinTitle = func(ctx context.Context, exe, dir, sessionID string) string {
	command := exec.CommandContext(ctx, exe, "list", "--format", "json")
	command.Dir = dir
	command.Stdin = nil
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = io.Discard
	hideWindow(command)
	if err := command.Run(); err != nil {
		return ""
	}
	return parseDevinListTitle(stdout.Bytes(), sessionID)
}
