package discord

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/brbbruno/agent-bridge/internal/channel"
	"github.com/bwmarrin/discordgo"
)

var (
	refFilePattern    = regexp.MustCompile(`<ref_file file="([^"]+)" />`)
	refSnippetPattern = regexp.MustCompile(`<ref_snippet file="([^"]+)" lines="([^"]+)" />`)
)

func keyboardComponents(keyboard channel.Keyboard, logf func(string, ...any)) ([]discordgo.MessageComponent, error) {
	if len(keyboard) == 0 {
		return nil, nil
	}
	buttons := make([]discordgo.MessageComponent, 0)
	for _, row := range keyboard {
		for _, button := range row {
			if len(button.Data) == 0 || len(button.Data) > 100 {
				return nil, errors.New("custom_id do botão Discord deve ter de 1 a 100 bytes")
			}
			style := discordgo.SecondaryButton
			switch button.Style {
			case "success":
				style = discordgo.SuccessButton
			case "danger":
				style = discordgo.DangerButton
			case "primary":
				style = discordgo.PrimaryButton
			}
			buttons = append(buttons, discordgo.Button{Label: truncateRunes(button.Text, 80), CustomID: button.Data, Style: style})
		}
	}
	maxButtons := 25
	if len(buttons) > maxButtons {
		if logf != nil {
			logf("Discord: botões excedentes ignorados (%d)", len(buttons)-maxButtons)
		}
		buttons = buttons[:maxButtons]
	}
	rows := make([]discordgo.MessageComponent, 0, (len(buttons)+4)/5)
	for start := 0; start < len(buttons); start += 5 {
		end := start + 5
		if end > len(buttons) {
			end = len(buttons)
		}
		rows = append(rows, discordgo.ActionsRow{Components: buttons[start:end]})
	}
	return rows, nil
}

func convertReferenceTags(text string) string {
	text = refSnippetPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := refSnippetPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return "`" + pathBase(parts[1]) + ":" + parts[2] + "`"
	})
	return refFilePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := refFilePattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		return "`" + pathBase(parts[1]) + "`"
	})
}

func pathBase(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	return filepath.Base(path)
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}
