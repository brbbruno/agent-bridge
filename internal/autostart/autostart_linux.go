//go:build linux

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func desktopFilePath(options Options) string {
	return filepath.Join(options.ConfigDir, "autostart", "agent-bridge.desktop")
}

func desktopQuote(value string) string {
	value = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(value)
	return `"` + value + `"`
}

func command(executable string) string { return desktopQuote(executable) + " daemon start" }

func Enable(options Options) (Status, error) {
	if options.Executable == "" || options.ConfigDir == "" {
		return Status{}, fmt.Errorf("diretório de configuração ou executável ausente")
	}
	data := []byte("[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=agent-bridge\n" +
		"Exec=" + command(options.Executable) + "\n" +
		"Terminal=false\n" +
		"NoDisplay=true\n" +
		"X-GNOME-Autostart-enabled=true\n")
	path := desktopFilePath(options)
	if err := writeAtomic(path, data); err != nil {
		return Status{}, err
	}
	return Get(options)
}

func Disable(options Options) error {
	err := os.Remove(desktopFilePath(options))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func Get(options Options) (Status, error) {
	path := desktopFilePath(options)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{Location: path}, nil
	}
	if err != nil {
		return Status{}, err
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			values[key] = value
		}
	}
	registered := values["Exec"]
	executable, valid := parseCommand(registered)
	status := Status{Location: path, Command: registered}
	status.Enabled = values["Type"] == "Application" && values["Name"] == "agent-bridge" && valid && values["Terminal"] == "false" && values["NoDisplay"] == "true" && values["X-GNOME-Autostart-enabled"] == "true"
	if status.Enabled {
		status.MatchesExecutable = filepath.Clean(executable) == filepath.Clean(options.Executable)
	}
	return status, nil
}

func parseCommand(value string) (string, bool) {
	if !strings.HasPrefix(value, `"`) {
		return "", false
	}
	var executable strings.Builder
	for index := 1; index < len(value); index++ {
		character := value[index]
		if character == '"' {
			return executable.String(), strings.TrimSpace(value[index+1:]) == "daemon start"
		}
		if character == '\\' {
			index++
			if index >= len(value) {
				return "", false
			}
			character = value[index]
		}
		executable.WriteByte(character)
	}
	return "", false
}
