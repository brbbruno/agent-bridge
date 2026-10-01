package telegram

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	linkPattern       = regexp.MustCompile(`(?i)^\[(.+?)\]\((https?://[^)\s]+)\)`)
	refFilePattern    = regexp.MustCompile(`^<ref_file\s+file="([^"]+)"\s*/>`)
	refSnippetPattern = regexp.MustCompile(`^<ref_snippet\s+file="([^"]+)"\s+lines="([^"]+)"\s*/>`)
	textEscaper       = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attributeEscaper  = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

func renderHTML(source string) string {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = collapseNewlines(source)
	lines := strings.Split(source, "\n")
	blocks := make([]string, 0, len(lines))
	for index := 0; index < len(lines); {
		line := lines[index]
		opening := strings.TrimLeft(line, " ")
		if strings.HasPrefix(opening, "```") {
			end := index + 1
			for end < len(lines) && strings.TrimSpace(lines[end]) != "```" {
				end++
			}
			language := ""
			if fields := strings.Fields(strings.TrimSpace(opening[3:])); len(fields) > 0 {
				language = fields[0]
			}
			bodyEnd := end
			if end == len(lines) {
				bodyEnd = len(lines)
			}
			bodyLines := append([]string(nil), lines[index+1:bodyEnd]...)
			indentation := line[:len(line)-len(opening)]
			if indentation != "" {
				for bodyIndex, bodyLine := range bodyLines {
					if strings.HasPrefix(bodyLine, indentation) {
						bodyLines[bodyIndex] = strings.TrimPrefix(bodyLine, indentation)
					}
				}
			}
			body := strings.Join(bodyLines, "\n")
			if language == "" {
				blocks = append(blocks, "<pre>"+escapeText(body)+"</pre>")
			} else {
				blocks = append(blocks, `<pre><code class="language-`+escapeAttribute(language)+`">`+escapeText(body)+"</code></pre>")
			}
			if end < len(lines) {
				index = end + 1
			} else {
				index = len(lines)
			}
			continue
		}
		if strings.HasPrefix(line, "|") {
			end := index + 1
			for end < len(lines) && strings.HasPrefix(lines[end], "|") {
				end++
			}
			blocks = append(blocks, "<pre>"+escapeText(strings.Join(lines[index:end], "\n"))+"</pre>")
			index = end
			continue
		}
		if strings.HasPrefix(line, "> ") {
			quoted := make([]string, 0)
			for index < len(lines) && strings.HasPrefix(lines[index], "> ") {
				quoted = append(quoted, renderInline(lines[index][2:]))
				index++
			}
			blocks = append(blocks, "<blockquote>"+strings.Join(quoted, "\n")+"</blockquote>")
			continue
		}
		if isHorizontalRule(line) {
			blocks = append(blocks, "———")
			index++
			continue
		}
		if heading, ok := headingContent(line); ok {
			blocks = append(blocks, "<b>"+renderInline(heading)+"</b>")
			index++
			continue
		}
		if bullet, ok := bulletContent(line); ok {
			blocks = append(blocks, bullet.prefix+"• "+renderInline(bullet.text))
			index++
			continue
		}
		if ordered, ok := orderedContent(line); ok {
			blocks = append(blocks, ordered.prefix+ordered.marker+renderInline(ordered.text))
			index++
			continue
		}
		blocks = append(blocks, renderInline(line))
		index++
	}
	return strings.Join(blocks, "\n")
}

type listContent struct {
	prefix string
	marker string
	text   string
}

func headingContent(line string) (string, bool) {
	count := 0
	for count < len(line) && count < 6 && line[count] == '#' {
		count++
	}
	if count == 0 || count >= len(line) || line[count] != ' ' {
		return "", false
	}
	return line[count+1:], true
}

func bulletContent(line string) (listContent, bool) {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	if len(line) < spaces+2 || (line[spaces] != '-' && line[spaces] != '*' && line[spaces] != '+') || line[spaces+1] != ' ' {
		return listContent{}, false
	}
	return listContent{prefix: line[:spaces], text: line[spaces+2:]}, true
}

func orderedContent(line string) (listContent, bool) {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	end := spaces
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	if end == spaces || end+1 >= len(line) || line[end] != '.' || line[end+1] != ' ' {
		return listContent{}, false
	}
	return listContent{prefix: line[:spaces], marker: line[spaces : end+2], text: line[end+2:]}, true
}

func isHorizontalRule(line string) bool {
	switch strings.TrimSpace(line) {
	case "---", "***", "___":
		return true
	default:
		return false
	}
}

func renderInline(source string) string {
	var output strings.Builder
	var plain strings.Builder
	flush := func() {
		output.WriteString(escapeText(plain.String()))
		plain.Reset()
	}
	for index := 0; index < len(source); {
		if matched, end := matchReference(source, index); end > index {
			flush()
			output.WriteString(matched)
			index = end
			continue
		}
		if source[index] == '[' {
			if match := linkPattern.FindStringSubmatchIndex(source[index:]); match != nil && match[0] == 0 {
				flush()
				label := source[index+match[2] : index+match[3]]
				url := source[index+match[4] : index+match[5]]
				output.WriteString(`<a href="` + escapeAttribute(url) + `">` + renderInline(label) + `</a>`)
				index += match[1]
				continue
			}
		}
		if source[index] == '`' {
			if end := strings.IndexByte(source[index+1:], '`'); end > 0 {
				flush()
				end += index + 1
				output.WriteString("<code>" + escapeText(source[index+1:end]) + "</code>")
				index = end + 1
				continue
			}
		}
		if index+1 < len(source) {
			marker := source[index : index+2]
			if marker == "**" || marker == "__" || marker == "~~" {
				if end := findInlineClosing(source, index+2, marker, false); end >= 0 {
					flush()
					tag := map[string]string{"**": "b", "__": "b", "~~": "s"}[marker]
					output.WriteString("<" + tag + ">" + renderInline(source[index+2:end]) + "</" + tag + ">")
					index = end + len(marker)
					continue
				}
			}
		}
		if source[index] == '*' || source[index] == '_' {
			marker := source[index : index+1]
			if italicOpening(source, index) {
				if end := findInlineClosing(source, index+1, marker, true); end >= 0 {
					flush()
					output.WriteString("<i>" + renderInline(source[index+1:end]) + "</i>")
					index = end + 1
					continue
				}
			}
		}
		runeValue, runeSize := utf8.DecodeRuneInString(source[index:])
		plain.WriteRune(runeValue)
		index += runeSize
	}
	flush()
	return output.String()
}

func matchReference(source string, start int) (string, int) {
	if strings.HasPrefix(source[start:], "<ref_file") {
		if match := refFilePattern.FindStringSubmatchIndex(source[start:]); match != nil && match[0] == 0 {
			path := source[start+match[2] : start+match[3]]
			return "<code>" + escapeText(pathBase(path)) + "</code>", start + match[1]
		}
	}
	if strings.HasPrefix(source[start:], "<ref_snippet") {
		if match := refSnippetPattern.FindStringSubmatchIndex(source[start:]); match != nil && match[0] == 0 {
			path := source[start+match[2] : start+match[3]]
			lines := source[start+match[4] : start+match[5]]
			return "<code>" + escapeText(pathBase(path)+":"+lines) + "</code>", start + match[1]
		}
	}
	return "", start
}

func pathBase(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func findInlineClosing(source string, start int, marker string, italic bool) int {
	for search := start; search < len(source); {
		index := strings.Index(source[search:], marker)
		if index < 0 {
			return -1
		}
		index += search
		if italic && !italicClosing(source, index) {
			search = index + len(marker)
			continue
		}
		return index
	}
	return -1
}

func italicOpening(source string, index int) bool {
	if index+1 >= len(source) || source[index+1] == source[index] || (index > 0 && source[index-1] == source[index]) {
		return false
	}
	if index > 0 {
		previous, _ := utf8.DecodeLastRuneInString(source[:index])
		if unicode.IsLetter(previous) || unicode.IsDigit(previous) {
			return false
		}
	}
	next, _ := utf8.DecodeRuneInString(source[index+1:])
	return !unicode.IsSpace(next)
}

func italicClosing(source string, index int) bool {
	marker := source[index]
	if (index > 0 && source[index-1] == marker) || (index+1 < len(source) && source[index+1] == marker) {
		return false
	}
	if index == 0 {
		return false
	}
	previous, _ := utf8.DecodeLastRuneInString(source[:index])
	if unicode.IsSpace(previous) {
		return false
	}
	if index+1 == len(source) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(source[index+1:])
	return !unicode.IsLetter(next) && !unicode.IsDigit(next)
}

func collapseNewlines(source string) string {
	var output strings.Builder
	for index := 0; index < len(source); {
		if source[index] != '\n' {
			output.WriteByte(source[index])
			index++
			continue
		}
		end := index + 1
		for end < len(source) && source[end] == '\n' {
			end++
		}
		count := end - index
		if count > 2 {
			count = 2
		}
		output.WriteString(strings.Repeat("\n", count))
		index = end
	}
	return output.String()
}

func escapeText(text string) string {
	return textEscaper.Replace(text)
}

func escapeAttribute(text string) string {
	return attributeEscaper.Replace(text)
}
