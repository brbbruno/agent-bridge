package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/model"
)

func Parse(kind model.EventType, agent model.Agent, env map[string]string, data []byte) (model.Event, error) {
	if agent != model.AgentDevin && agent != model.AgentClaude {
		return model.Event{}, fmt.Errorf("agente desconhecido: %s", agent)
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return model.Event{}, fmt.Errorf("payload JSON inválido: %w", err)
	}
	if payload == nil {
		return model.Event{}, errors.New("payload JSON deve ser um objeto")
	}
	toolName := stringValue(payload["tool_name"])
	if kind == model.EventQuestion && !isQuestionTool(agent, toolName) {
		return model.Event{}, fmt.Errorf("ferramenta de pergunta inesperada: %q", toolName)
	}

	cwd := ""
	if agent == model.AgentDevin {
		cwd = env["DEVIN_PROJECT_DIR"]
	} else {
		cwd = env["CLAUDE_PROJECT_DIR"]
	}
	if cwd == "" {
		cwd = stringValue(payload["cwd"])
	}
	project := baseName(cwd)
	if project == "" {
		project = "projeto"
	}
	sessionID := stringValue(payload["session_id"])
	if sessionID == "" {
		sessionID = "sessão-desconhecida"
	}

	event := model.Event{
		Agent:       agent,
		Type:        kind,
		SessionID:   sessionID,
		SessionName: shortSessionName(sessionID),
		TurnID:      stringValue(payload["prompt_id"]),
		Project:     project,
		CWD:         cwd,
		ToolName:    toolName,
		ToolUseID:   stringValue(payload["tool_use_id"]),
		OccurredAt:  time.Now(),
	}
	toolInput := objectValue(payload["tool_input"])
	switch kind {
	case model.EventStop:
		event.Message = stringValue(payload["last_assistant_message"])
		event.StopActive, _ = payload["stop_hook_active"].(bool)
	case model.EventPermission:
		event.ToolSummary = summarizeTool(toolName, toolInput)
	case model.EventQuestion:
		event.Questions = parseQuestions(toolInput["questions"])
		if len(event.Questions) == 0 {
			return model.Event{}, errors.New("payload da ferramenta de perguntas não contém questions")
		}
		event.ToolSummary = summarizeTool(toolName, toolInput)
	case model.EventPrompt:
		event.Prompt = stringValue(payload["prompt"])
	case model.EventSessionEnd:
		event.Message = stringValue(payload["reason"])
	case model.EventProgress:
		event.ToolSummary = summarizeProgressTool(toolInput)
		if response, ok := payload["tool_response"].(map[string]any); ok {
			if success, exists := response["success"].(bool); exists {
				event.ToolFailed = !success
			}
		}
	default:
		return model.Event{}, fmt.Errorf("tipo de evento inválido: %s", kind)
	}
	return event, nil
}

func isQuestionTool(agent model.Agent, name string) bool {
	if agent == model.AgentDevin {
		return strings.EqualFold(name, "ask_user_question")
	}
	return name == "AskUserQuestion"
}

func parseQuestions(value any) []model.Question {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	questions := make([]model.Question, 0, len(items))
	for _, item := range items {
		object := objectValue(item)
		text := strings.TrimSpace(stringValue(object["question"]))
		if text == "" {
			continue
		}
		question := model.Question{Text: text, Header: stringValue(object["header"])}
		question.MultiSelect, _ = object["multi_select"].(bool)
		if value, ok := object["multiSelect"].(bool); ok {
			question.MultiSelect = value
		}
		if options, ok := object["options"].([]any); ok {
			for _, optionValue := range options {
				option := objectValue(optionValue)
				label := strings.TrimSpace(stringValue(option["label"]))
				if label == "" {
					continue
				}
				question.Options = append(question.Options, model.Option{Label: label, Description: stringValue(option["description"])})
			}
		}
		questions = append(questions, question)
	}
	return questions
}

func summarizeProgressTool(input map[string]any) string {
	if command := stringValue(input["command"]); strings.TrimSpace(command) != "" {
		firstLine := strings.TrimSpace(strings.SplitN(command, "\n", 2)[0])
		return truncateSummary(firstLine, 120)
	}
	for _, key := range []string{"file_path", "path", "notebook_path"} {
		if path := strings.TrimSpace(stringValue(input[key])); path != "" {
			return baseName(path)
		}
	}
	for _, key := range []string{"pattern", "query", "url"} {
		if value := strings.TrimSpace(stringValue(input[key])); value != "" {
			return truncateSummary(value, 60)
		}
	}
	return ""
}

func truncateSummary(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 0 {
		return ""
	}
	return string(runes[:limit-1]) + "…"
}

func summarizeTool(name string, input map[string]any) string {
	if command := stringValue(input["command"]); command != "" {
		return truncate(command, 360)
	}
	if len(input) == 0 {
		return name
	}
	data, err := json.Marshal(input)
	if err != nil {
		return name
	}
	return truncate(string(data), 360)
}

func objectValue(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func baseName(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	path = strings.TrimRight(path, "/")
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

func shortSessionName(id string) string {
	id = strings.TrimSpace(id)
	if len([]rune(id)) <= 24 {
		return id
	}
	runes := []rune(id)
	return "…" + string(runes[len(runes)-20:])
}
