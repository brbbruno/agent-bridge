package channel

import (
	"strings"
	"unicode/utf8"
)

func SplitText(text string, limit int) []string {
	if limit <= 0 {
		limit = MaxMessageRunes
	}
	if text == "" {
		return []string{""}
	}
	if strings.Contains(text, "```") {
		return splitTextWithFences(text, limit)
	}
	return splitTextPlain(text, limit)
}

func splitTextPlain(text string, limit int) []string {
	chunks := make([]string, 0, utf8.RuneCountInString(text)/limit+1)
	remaining := []rune(text)
	for len(remaining) > limit {
		cut := preferredTextCut(remaining, limit)
		chunks = append(chunks, string(remaining[:cut]))
		remaining = remaining[cut:]
	}
	if len(remaining) > 0 {
		chunks = append(chunks, string(remaining))
	}
	return chunks
}

func splitTextWithFences(text string, limit int) []string {
	source := []rune(text)
	chunks := make([]string, 0, len(source)/limit+1)
	position := 0
	prefix := ""
	closingFence := "\n```"
	for position < len(source) {
		prefixLength := utf8.RuneCountInString(prefix)
		budget := limit - prefixLength
		if budget <= 0 {
			budget = limit
			prefix = ""
			prefixLength = 0
		}
		remaining := source[position:]
		cut := preferredTextCut(remaining, budget)
		cut = keepFenceMarkerLineTogether(source, position, cut)
		if cut <= 0 {
			cut = preferredTextCut(remaining, budget)
		}
		if cut == len(remaining) {
			chunks = append(chunks, prefix+string(remaining))
			break
		}
		insideFence, openingLine := fenceStateAt(source, position+cut)
		suffix := ""
		if insideFence {
			closingBudget := limit - prefixLength - utf8.RuneCountInString(closingFence)
			if closingBudget > 0 {
				cut = preferredTextCut(remaining, closingBudget)
				cut = keepFenceMarkerLineTogether(source, position, cut)
				if cut <= 0 {
					cut = preferredTextCut(remaining, closingBudget)
				}
				insideFence, openingLine = fenceStateAt(source, position+cut)
				if insideFence {
					suffix = closingFence
				}
			}
		}
		chunk := prefix + string(source[position:position+cut]) + suffix
		chunks = append(chunks, chunk)
		position += cut
		if suffix != "" {
			prefix = openingLine
		} else {
			prefix = ""
		}
	}
	return chunks
}

func preferredTextCut(remaining []rune, limit int) int {
	if len(remaining) <= limit {
		return len(remaining)
	}
	cut := limit
	for index := limit - 1; index > limit*3/4; index-- {
		if remaining[index] == '\n' || remaining[index] == ' ' {
			cut = index + 1
			break
		}
	}
	return cut
}

func keepFenceMarkerLineTogether(source []rune, start, cut int) int {
	boundary := start + cut
	if boundary >= len(source) || boundary <= start {
		return cut
	}
	lineStart := boundary - 1
	for lineStart >= 0 && source[lineStart] != '\n' {
		lineStart--
	}
	lineStart++
	if lineStart < start {
		return cut
	}
	lineEnd := lineStart
	for lineEnd < len(source) && source[lineEnd] != '\n' {
		lineEnd++
	}
	line := string(source[lineStart:lineEnd])
	if !strings.HasPrefix(strings.TrimLeft(line, " "), "```") {
		return cut
	}
	if boundary < lineEnd && lineStart > start {
		return lineStart - start
	}
	return cut
}

func fenceStateAt(source []rune, end int) (bool, string) {
	inside := false
	openingLine := ""
	for start := 0; start < end; {
		lineEnd := start
		for lineEnd < end && source[lineEnd] != '\n' {
			lineEnd++
		}
		complete := lineEnd < end
		line := string(source[start:lineEnd])
		if !inside && strings.HasPrefix(strings.TrimLeft(line, " "), "```") {
			inside = true
			fullLineEnd := lineEnd
			for fullLineEnd < len(source) && source[fullLineEnd] != '\n' {
				fullLineEnd++
			}
			openingLine = string(source[start:fullLineEnd])
			if fullLineEnd < len(source) {
				openingLine += "\n"
			}
		} else if inside && strings.TrimSpace(line) == "```" {
			inside = false
			openingLine = ""
		}
		if !complete {
			break
		}
		start = lineEnd + 1
	}
	return inside, openingLine
}
