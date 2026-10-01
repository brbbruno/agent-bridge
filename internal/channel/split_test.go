package channel

import (
	"strings"
	"testing"
)

func TestSplitTextLimit(t *testing.T) {
	text := strings.Repeat("a", 9000)
	chunks := SplitText(text, MaxMessageRunes)
	if len(chunks) != 3 {
		t.Fatalf("len(chunks)=%d", len(chunks))
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > MaxMessageRunes {
			t.Fatalf("chunk has %d runes", len([]rune(chunk)))
		}
	}
}

func TestSplitTextKeepsFencedCodeValidAcrossChunks(t *testing.T) {
	text := "```go\n" + strings.Repeat("x", 100)
	chunks := SplitText(text, 50)
	if len(chunks) < 2 {
		t.Fatalf("esperava múltiplos chunks: %d", len(chunks))
	}
	if !strings.HasSuffix(chunks[0], "\n```") {
		t.Fatalf("primeiro chunk não fechou o fence: %q", chunks[0])
	}
	if !strings.HasPrefix(chunks[1], "```go\n") {
		t.Fatalf("segundo chunk não reabriu o fence: %q", chunks[1])
	}
	for index, chunk := range chunks {
		if got := len([]rune(chunk)); got > 50 {
			t.Fatalf("chunk %d excedeu limite: %d", index, got)
		}
	}
}

func TestSplitTextReopensIndentedFenceAcrossChunks(t *testing.T) {
	opening := "   ```go\n"
	text := opening + "   " + strings.Repeat("x", 100)
	chunks := SplitText(text, 50)
	if len(chunks) < 2 {
		t.Fatalf("esperava múltiplos chunks: %d", len(chunks))
	}
	if !strings.HasSuffix(chunks[0], "\n```") {
		t.Fatalf("primeiro chunk não fechou o fence: %q", chunks[0])
	}
	if !strings.HasPrefix(chunks[1], opening) {
		t.Fatalf("segundo chunk não reabriu o fence indentado: %q", chunks[1])
	}
	for index, chunk := range chunks {
		if got := len([]rune(chunk)); got > 50 {
			t.Fatalf("chunk %d excedeu limite: %d", index, got)
		}
	}
}
