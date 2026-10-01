package hook

import (
	"encoding/json"
	"errors"

	"github.com/brbbruno/agent-bridge/internal/model"
)

func Encode(agent model.Agent, kind model.EventType, result model.Resolution) ([]byte, error) {
	if kind == model.EventProgress {
		return nil, nil
	}
	if result.Action == "" || result.Action == model.ActionNone {
		return nil, nil
	}
	if agent != model.AgentDevin && agent != model.AgentClaude {
		return nil, errors.New("agente desconhecido")
	}
	var output any
	switch kind {
	case model.EventPrompt:
		if result.Action != model.ActionContext || result.Context == "" {
			return nil, nil
		}
		output = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "UserPromptSubmit", "additionalContext": result.Context}}
	case model.EventStop:
		if result.Action != model.ActionBlock {
			return nil, nil
		}
		output = blockOutput(result.Reason)
	case model.EventPermission:
		if agent == model.AgentDevin {
			if result.Action == model.ActionApprove {
				output = map[string]any{"decision": "approve"}
			} else if result.Action == model.ActionDeny || result.Action == model.ActionBlock {
				output = blockOutput(result.Reason)
			}
		} else {
			behavior := "deny"
			if result.Action == model.ActionApprove {
				behavior = "allow"
			} else if result.Action != model.ActionDeny && result.Action != model.ActionBlock {
				return nil, nil
			}
			decision := map[string]any{"behavior": behavior}
			if behavior == "deny" && result.Reason != "" {
				decision["message"] = result.Reason
			}
			output = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": decision}}
		}
	case model.EventQuestion:
		if result.Action != model.ActionBlock && result.Action != model.ActionDeny {
			return nil, nil
		}
		if agent == model.AgentDevin {
			output = blockOutput(result.Reason)
		} else {
			output = map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": result.Reason,
			}}
		}
	default:
		return nil, errors.New("evento sem formato de saída suportado")
	}
	if output == nil {
		return nil, nil
	}
	data, err := json.Marshal(output)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func blockOutput(reason string) map[string]any {
	if reason == "" {
		reason = "O usuário respondeu pelo celular."
	}
	return map[string]any{"decision": "block", "reason": reason}
}
