package telegram_test

import (
	"strings"
	"testing"

	"bria/internal/telegram"
)

func TestNormalizeRichMarkdownCompactsTable(t *testing.T) {
	got := telegram.NormalizeRichMarkdown("Статус\n\n\u00a0\n\n| Сервер | Бэк |\n|---|---|\n| local | codex |")
	if !strings.Contains(got, "| <sub>Сервер</sub> | <sub>Бэк</sub> |") || !strings.Contains(got, "| <sub>local</sub> | <sub>codex</sub> |") {
		t.Fatalf("normalized table = %q", got)
	}
}

func TestNormalizeRichMarkdownConvertsShellFenceForRichClients(t *testing.T) {
	input := "<details><summary>✓ exec</summary>\n\n```shell\nprintf '<tag>&value\\n'\n```\n\n---\n\nplain &lt;result&gt;&amp;\n\n</details>"
	want := "<details><summary>✓ exec</summary>\n\n<pre><code class=\"language-shell\">printf &#39;&lt;tag&gt;&amp;value\\n&#39;</code></pre>\n\n---\n\nplain &lt;result&gt;&amp;\n\n</details>"
	if got := telegram.NormalizeRichMarkdown(input); got != want {
		t.Fatalf("normalized shell block = %q, want %q", got, want)
	}
}

func TestNormalizeRichMarkdownPreservesEscapesAndNonTables(t *testing.T) {
	cases := []struct{ input, want string }{
		{"Before\n| A | B |\n|:---|---:|\n| x\\|y |  |\nAfter", "Before\n\n| <sub>A</sub> | <sub>B</sub> |\n|:---|---:|\n| <sub>x\\|y</sub> |  |\nAfter"},
		{"| ordinary | text |\n| not | separator |", "| ordinary | text |\n| not | separator |"},
		{"plain\ntext", "plain\ntext"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := telegram.NormalizeRichMarkdown(tc.input); got != tc.want {
			t.Errorf("normalize %q = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeRichMarkdownTwoTablesIsIdempotent(t *testing.T) {
	input := "| A | B |\n|---|---:|\n| x\\|y | z |\n\nSecond\n| C | D |\n|---|---|\n| <sub>ready</sub> | end |"
	want := "\n\n| <sub>A</sub> | <sub>B</sub> |\n|---|---:|\n| <sub>x\\|y</sub> | <sub>z</sub> |\n\nSecond\n\n| <sub>C</sub> | <sub>D</sub> |\n|---|---|\n| <sub>ready</sub> | <sub>end</sub> |"
	got := telegram.NormalizeRichMarkdown(input)
	if got != want {
		t.Fatalf("two tables = %q, want %q", got, want)
	}
	if again := telegram.NormalizeRichMarkdown(got); again != got {
		t.Fatalf("normalization changed on second pass: %q", again)
	}
}

func TestNormalizeRichMarkdownPreservesLiteralTables(t *testing.T) {
	const table = "| A | B |\n|---|---|\n| one | two |"
	for name, literal := range map[string]string{
		"tilde fence":         "~~~markdown\n" + table + "\n~~~",
		"inline backticks":    "`" + table + "`",
		"multiline backticks": "``\n" + table + "\n``",
		"code tag":            "<code>\n" + table + "\n</code>",
		"code attributes":     "<pre><code class=\"language-markdown\">\n" + table + "\n</code></pre>",
		"pre tag":             "<PRE>\n" + table + "\n</PRE>",
		"unclosed fence":      "~~~\n" + table,
		"unclosed code":       "<code>\n" + table,
	} {
		t.Run(name, func(t *testing.T) {
			if got := telegram.NormalizeRichMarkdown(literal); got != literal {
				t.Fatalf("literal changed: %q, want %q", got, literal)
			}
		})
	}
	if got, want := telegram.NormalizeRichMarkdown("```markdown\n"+table+"\n```"), `<pre><code class="language-markdown">`+table+`</code></pre>`; got != want {
		t.Fatalf("backtick code table = %q, want %q", got, want)
	}
	longFence := "````markdown\n```\n" + table + "\n```\n````"
	if got, want := telegram.NormalizeRichMarkdown(longFence), `<pre><code class="language-markdown">`+"```\n"+table+"\n```"+`</code></pre>`; got != want {
		t.Fatalf("long backtick code table = %q, want %q", got, want)
	}
}

func TestNormalizeRichMarkdownSelectsOnlyRealTables(t *testing.T) {
	const table = "| A | B |\n|---|---:|\n| x\\|y | z |"
	const compact = "| <sub>A</sub> | <sub>B</sub> |\n|---|---:|\n| <sub>x\\|y</sub> | <sub>z</sub> |"
	for name, tc := range map[string]struct {
		text string
		want string
	}{
		"table":               {table, "\n\n" + compact},
		"two tables":          {table + "\n\nSecond\n" + table, "\n\n" + compact + "\n\nSecond\n\n" + compact},
		"inline code cell":    {"| `A` | B |\n|---|---|\n| x | y |", "\n\n| <sub>`A`</sub> | <sub>B</sub> |\n|---|---|\n| <sub>x</sub> | <sub>y</sub> |"},
		"pipe prose":          {"| prose | text |\n| still | prose |", "| prose | text |\n| still | prose |"},
		"escaped pipes only":  {"| A \\| B |\n|---|---|", "| A \\| B |\n|---|---|"},
		"backtick fence":      {"```md\n" + table + "\n```", `<pre><code class="language-md">` + table + `</code></pre>`},
		"tilde fence":         {"~~~md\n" + table + "\n~~~", "~~~md\n" + table + "\n~~~"},
		"multiline code span": {"``\n" + table + "\n``", "``\n" + table + "\n``"},
		"HTML code":           {"<pre><code class=\"language-md\">\n" + table + "\n</code></pre>", "<pre><code class=\"language-md\">\n" + table + "\n</code></pre>"},
		"table after literal": {"~~~\n" + table + "\n~~~\n\n" + table, "~~~\n" + table + "\n~~~\n\n" + compact},
		"table after HTML":    {"<code>\n" + table + "\n</code>\n" + table, "<code>\n" + table + "\n</code>\n\n" + compact},
		"empty":               {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := telegram.NormalizeRichMarkdown(tc.text); got != tc.want {
				t.Fatalf("NormalizeRichMarkdown(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
