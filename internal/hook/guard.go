package hook

// IsClaudeUnderDevin uses Devin's documented hook environment marker. CLAUDE_PROJECT_DIR
// is not required to be absent; some compatibility paths may set both variables.
func IsClaudeUnderDevin(env map[string]string) bool {
	return env["DEVIN_PROJECT_DIR"] != ""
}
