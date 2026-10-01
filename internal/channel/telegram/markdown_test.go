package telegram

import (
	"html"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderHTML(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "inline formatting", source: "**negrito** *itálico* ~~riscado~~ `código` __forte__ _ênfase_", want: "<b>negrito</b> <i>itálico</i> <s>riscado</s> <code>código</code> <b>forte</b> <i>ênfase</i>"},
		{name: "literal underscores and stars", source: "snake_case_name and 2*3*4 and rm *.txt", want: "snake_case_name and 2*3*4 and rm *.txt"},
		{name: "escape text", source: "a < b & c > d", want: "a &lt; b &amp; c &gt; d"},
		{name: "links", source: "[site](https://example.test/a?x=1&y=2) [x](file:///c)", want: `<a href="https://example.test/a?x=1&amp;y=2">site</a> [x](file:///c)`},
		{name: "fenced code with language", source: "```go\nfmt.Println(\"<oi>\")\n```", want: `<pre><code class="language-go">fmt.Println("&lt;oi&gt;")</code></pre>`},
		{name: "fenced code without language", source: "```\na < b\n```", want: "<pre>a &lt; b</pre>"},
		{name: "indented fence in ordered item", source: "1. **Agora:** o cabeçalho deve ser:\n   ```\n   Devin · BRUNO-PC\n   ```\n   - depois", want: "1. <b>Agora:</b> o cabeçalho deve ser:\n<pre>Devin · BRUNO-PC</pre>\n   • depois"},
		{name: "indented fence with language", source: "  ```go\n  fmt.Println(1)\n  ```", want: `<pre><code class="language-go">fmt.Println(1)</code></pre>`},
		{name: "unclosed fence", source: "```go\nfmt.Println(1)", want: `<pre><code class="language-go">fmt.Println(1)</code></pre>`},
		{name: "empty inline code stays literal", source: "a `` b", want: "a `` b"},
		{name: "heading", source: "## Título **forte**", want: "<b>Título <b>forte</b></b>"},
		{name: "bullets and ordered list", source: "  - Primeiro\n* Segundo\n+ Terceiro\n1. **Quarto**", want: "  • Primeiro\n• Segundo\n• Terceiro\n1. <b>Quarto</b>"},
		{name: "merged blockquote", source: "> Primeiro\n> **segundo**", want: "<blockquote>Primeiro\n<b>segundo</b></blockquote>"},
		{name: "horizontal rules", source: "---\n***\n___", want: "———\n———\n———"},
		{name: "table", source: "| a | b |\n|---|---|\n| x | y |", want: "<pre>| a | b |\n|---|---|\n| x | y |</pre>"},
		{name: "Devin references", source: `<ref_file file="C:\a\b\router.go" /> <ref_snippet file="C:\a\b\router.go" lines="2-4" />`, want: "<code>router.go</code> <code>router.go:2-4</code>"},
		{name: "unmatched marker", source: "**open", want: "**open"},
		{name: "collapse newlines", source: "a\n\n\n\nb", want: "a\n\nb"},
	}
	stripTags := regexp.MustCompile(`<[^>]*>`)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := renderHTML(test.source)
			if got != test.want {
				t.Fatalf("renderHTML() = %q, want %q", got, test.want)
			}
			visible := html.UnescapeString(stripTags.ReplaceAllString(got, ""))
			if utf8.RuneCountInString(visible) > utf8.RuneCountInString(test.source) {
				t.Fatalf("visible text grew: %d > %d: %q", utf8.RuneCountInString(visible), utf8.RuneCountInString(test.source), visible)
			}
		})
	}
	if got := renderHTML(strings.Repeat("\n", 4)); got != "\n\n" {
		t.Fatalf("linhas em branco=%q", got)
	}
}
