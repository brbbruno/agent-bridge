package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brbbruno/agent-bridge/internal/config"
	"github.com/brbbruno/agent-bridge/internal/model"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseSpikeAndClaudeFixtures(t *testing.T) {
	devinStop, err := Parse(model.EventStop, model.AgentDevin, map[string]string{"DEVIN_PROJECT_DIR": `C:\work\demo`}, fixture(t, "devin-stop.json"))
	if err != nil {
		t.Fatal(err)
	}
	if devinStop.SessionID != "general-gull" || devinStop.Project != "demo" || devinStop.Message != "MAPLE" || devinStop.StopActive {
		t.Fatalf("payload Devin inesperado: %+v", devinStop)
	}
	devinPermission, err := Parse(model.EventPermission, model.AgentDevin, nil, fixture(t, "devin-permission.json"))
	if err != nil || devinPermission.ToolName != "exec" || !strings.Contains(devinPermission.ToolSummary, "permission-approved.txt") {
		t.Fatalf("PermissionRequest Devin inesperado: %+v, %v", devinPermission, err)
	}
	devinPrompt, err := Parse(model.EventPrompt, model.AgentDevin, nil, fixture(t, "devin-prompt.json"))
	if err != nil || devinPrompt.Prompt != "What is the secret word?" {
		t.Fatalf("UserPromptSubmit Devin inesperado: %+v, %v", devinPrompt, err)
	}
	devinEnd, err := Parse(model.EventSessionEnd, model.AgentDevin, nil, fixture(t, "devin-session-end.json"))
	if err != nil || devinEnd.Message != "other" {
		t.Fatalf("SessionEnd Devin inesperado: %+v, %v", devinEnd, err)
	}
	devinQuestion, err := Parse(model.EventQuestion, model.AgentDevin, map[string]string{"DEVIN_PROJECT_DIR": `C:\work\demo`}, fixture(t, "devin-question.json"))
	if err != nil {
		t.Fatal(err)
	}
	if devinQuestion.ToolName != "ask_user_question" || len(devinQuestion.Questions) != 1 || devinQuestion.Questions[0].Options[0].Label != "Sandwich" {
		t.Fatalf("pergunta Devin inesperada: %+v", devinQuestion)
	}
	claudeQuestion, err := Parse(model.EventQuestion, model.AgentClaude, map[string]string{}, fixture(t, "claude-question.json"))
	if err != nil {
		t.Fatal(err)
	}
	if claudeQuestion.ToolName != "AskUserQuestion" || claudeQuestion.Project != "demo" || len(claudeQuestion.Questions) != 1 || claudeQuestion.Questions[0].MultiSelect {
		t.Fatalf("pergunta Claude inesperada: %+v", claudeQuestion)
	}
	multiQuestion, err := Parse(model.EventQuestion, model.AgentClaude, nil, fixture(t, "claude-question-multiselect.json"))
	if err != nil || len(multiQuestion.Questions) != 1 || !multiQuestion.Questions[0].MultiSelect {
		t.Fatalf("multi-select Claude inesperado: %+v, %v", multiQuestion, err)
	}
	claudeStop, err := Parse(model.EventStop, model.AgentClaude, nil, fixture(t, "claude-stop.json"))
	if err != nil {
		t.Fatal(err)
	}
	if claudeStop.Message != "Refactor complete." || claudeStop.Project != "demo" {
		t.Fatalf("Stop Claude inesperado: %+v", claudeStop)
	}
	permission, err := Parse(model.EventPermission, model.AgentClaude, nil, fixture(t, "claude-permission.json"))
	if err != nil || permission.ToolSummary != "npm test" {
		t.Fatalf("PermissionRequest Claude inesperado: %+v, %v", permission, err)
	}
}

func TestParseRejectsWrongQuestionTool(t *testing.T) {
	payload := []byte(`{"tool_name":"read","tool_input":{"questions":[{"question":"x"}]}}`)
	if _, err := Parse(model.EventQuestion, model.AgentDevin, nil, payload); err == nil {
		t.Fatal("esperava erro para ferramenta não-question")
	}
}

func TestClaudeUnderDevinGuard(t *testing.T) {
	if !IsClaudeUnderDevin(map[string]string{"DEVIN_PROJECT_DIR": `C:\work\demo`, "CLAUDE_PROJECT_DIR": `C:\work\demo`}) {
		t.Fatal("deve preferir o marcador positivo de Devin mesmo se ambos existirem")
	}
	if IsClaudeUnderDevin(map[string]string{"CLAUDE_PROJECT_DIR": `C:\work\demo`}) {
		t.Fatal("CLAUDE_PROJECT_DIR sozinho não identifica Devin")
	}
}

func TestClaudeHookNoopsUnderDevinAndRecordsEnvDetector(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENT_BRIDGE_HOME", home)
	output := Execute(model.EventStop, model.AgentClaude, []byte("not-json"), map[string]string{"DEVIN_PROJECT_DIR": `C:\\repo`, "CLAUDE_PROJECT_DIR": ""})
	if len(output) != 0 {
		t.Fatalf("hook duplicado produziu stdout: %q", output)
	}
	if _, err := os.Stat(config.TokenPath(home)); !os.IsNotExist(err) {
		t.Fatalf("guard tentou iniciar o daemon: %v", err)
	}
	logData, err := os.ReadFile(config.LogPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "CLAUDE_PROJECT_DIR presente=false") {
		t.Fatalf("log não registrou evidência do detector: %s", logData)
	}
}

func TestOutputShapesByAgentAndEvent(t *testing.T) {
	tests := []struct {
		name  string
		agent model.Agent
		kind  model.EventType
		input model.Resolution
		check func(map[string]any) bool
	}{
		{"devin stop block", model.AgentDevin, model.EventStop, model.Resolution{Action: model.ActionBlock, Reason: "segue"}, func(m map[string]any) bool { return m["decision"] == "block" && m["reason"] == "segue" }},
		{"devin permission allow", model.AgentDevin, model.EventPermission, model.Resolution{Action: model.ActionApprove}, func(m map[string]any) bool { return m["decision"] == "approve" }},
		{"devin permission deny", model.AgentDevin, model.EventPermission, model.Resolution{Action: model.ActionDeny, Reason: "não"}, func(m map[string]any) bool { return m["decision"] == "block" && m["reason"] == "não" }},
		{"devin question deny", model.AgentDevin, model.EventQuestion, model.Resolution{Action: model.ActionBlock, Reason: "resposta"}, func(m map[string]any) bool { return m["decision"] == "block" }},
		{"devin prompt", model.AgentDevin, model.EventPrompt, model.Resolution{Action: model.ActionContext, Context: "ausente"}, func(m map[string]any) bool {
			return nested(m, "hookSpecificOutput", "hookEventName") == "UserPromptSubmit" && nested(m, "hookSpecificOutput", "additionalContext") == "ausente"
		}},
		{"claude stop block", model.AgentClaude, model.EventStop, model.Resolution{Action: model.ActionBlock, Reason: "segue"}, func(m map[string]any) bool { return m["decision"] == "block" && m["reason"] == "segue" }},
		{"claude permission allow", model.AgentClaude, model.EventPermission, model.Resolution{Action: model.ActionApprove}, func(m map[string]any) bool {
			return nested(m, "hookSpecificOutput", "hookEventName") == "PermissionRequest" && nested(m, "hookSpecificOutput", "decision", "behavior") == "allow"
		}},
		{"claude permission deny", model.AgentClaude, model.EventPermission, model.Resolution{Action: model.ActionDeny, Reason: "negado"}, func(m map[string]any) bool {
			return nested(m, "hookSpecificOutput", "decision", "behavior") == "deny" && nested(m, "hookSpecificOutput", "decision", "message") == "negado"
		}},
		{"claude question deny", model.AgentClaude, model.EventQuestion, model.Resolution{Action: model.ActionBlock, Reason: "sem interação"}, func(m map[string]any) bool {
			return nested(m, "hookSpecificOutput", "hookEventName") == "PreToolUse" && nested(m, "hookSpecificOutput", "permissionDecision") == "deny" && nested(m, "hookSpecificOutput", "permissionDecisionReason") == "sem interação"
		}},
		{"claude prompt", model.AgentClaude, model.EventPrompt, model.Resolution{Action: model.ActionContext, Context: "ausente"}, func(m map[string]any) bool { return nested(m, "hookSpecificOutput", "additionalContext") == "ausente" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := Encode(test.agent, test.kind, test.input)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("JSON inválido %q: %v", data, err)
			}
			if !test.check(decoded) {
				t.Fatalf("saída inesperada: %s", data)
			}
		})
	}
}

func TestOutputNoneIsEmpty(t *testing.T) {
	data, err := Encode(model.AgentDevin, model.EventStop, model.Resolution{Action: model.ActionNone})
	if err != nil || len(data) != 0 {
		t.Fatalf("output=%q err=%v", data, err)
	}
}

func nested(value any, keys ...string) any {
	current := value
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}
