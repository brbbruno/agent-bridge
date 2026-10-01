package model

import "time"

type Agent string

const (
	AgentDevin  Agent = "devin"
	AgentClaude Agent = "claude"
)

type EventType string

const (
	EventStop       EventType = "stop"
	EventPermission EventType = "permission"
	EventQuestion   EventType = "question"
	EventPrompt     EventType = "prompt"
	EventSessionEnd EventType = "session-end"
)

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type Question struct {
	Text        string   `json:"text"`
	Header      string   `json:"header,omitempty"`
	Options     []Option `json:"options,omitempty"`
	MultiSelect bool     `json:"multi_select,omitempty"`
}

type Event struct {
	Agent        Agent      `json:"agent"`
	Type         EventType  `json:"type"`
	SessionID    string     `json:"session_id"`
	SessionName  string     `json:"session_name"`
	SessionTitle string     `json:"session_title,omitempty"`
	Project      string     `json:"project"`
	CWD          string     `json:"cwd,omitempty"`
	Message      string     `json:"message,omitempty"`
	ToolName     string     `json:"tool_name,omitempty"`
	ToolSummary  string     `json:"tool_summary,omitempty"`
	ToolUseID    string     `json:"tool_use_id,omitempty"`
	Questions    []Question `json:"questions,omitempty"`
	Prompt       string     `json:"prompt,omitempty"`
	StopActive   bool       `json:"stop_hook_active,omitempty"`
	OccurredAt   time.Time  `json:"occurred_at,omitempty"`
}

type Action string

const (
	ActionNone    Action = "none"
	ActionBlock   Action = "block"
	ActionApprove Action = "approve"
	ActionDeny    Action = "deny"
	ActionContext Action = "context"
)

type Resolution struct {
	Action  Action `json:"action"`
	Reason  string `json:"reason,omitempty"`
	Context string `json:"context,omitempty"`
}
