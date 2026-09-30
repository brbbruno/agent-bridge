package model

import "testing"

func TestEventConstants(t *testing.T) {
	if EventStop != "stop" || AgentDevin != "devin" || AgentClaude != "claude" {
		t.Fatal("constantes incompatíveis")
	}
}
