package cardtranscript

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTablePaginationRepeatsHeadersAndKeepsRows(t *testing.T) {
	for _, cell := range []string{"value", "данные東京🙂"} {
		t.Run(cell, func(t *testing.T) {
			const header = "| item | value |\n| :--- | ---: |\n"
			rows := make([]string, 80)
			for i := range rows {
				rows[i] = fmt.Sprintf("| row-%03d | %s |\n", i, strings.Repeat(cell, 20))
			}
			input := header + strings.Join(rows, "")
			parts := Split(input, 3000)
			if len(parts) < 2 {
				t.Fatal("expected continuation pages")
			}
			var data strings.Builder
			for i, part := range parts {
				if len(part) > 3000 || !utf8.ValidString(part) || !strings.HasPrefix(part, header) {
					t.Fatalf("page %d: bytes=%d, valid UTF-8=%v, repeated header=%v", i, len(part), utf8.ValidString(part), strings.HasPrefix(part, header))
				}
				body := strings.TrimPrefix(part, header)
				for _, row := range strings.SplitAfter(body, "\n") {
					if row != "" && (!strings.HasPrefix(row, "| row-") || !strings.HasSuffix(row, " |\n")) {
						t.Fatalf("split data row on page %d", i)
					}
				}
				data.WriteString(body)
			}
			if data.String() != strings.Join(rows, "") {
				t.Fatal("data rows changed, duplicated or lost")
			}
		})
	}
}

func TestTablePaginationMixedFinalPreservesContentAndBoundaries(t *testing.T) {
	const header = "| key | значение |\n| --- | --- |\n"
	var body strings.Builder
	body.WriteString("Synthetic introduction.\n\n" + header)
	for i := 0; i < 45; i++ {
		fmt.Fprintf(&body, "| entry-%02d | %s |\n", i, strings.Repeat("東京-я🙂", 13))
	}
	body.WriteString("\nSynthetic conclusion.")
	want := body.String()
	pages := Paginate(RenderBlocks([]Block{
		{Kind: "commentary", Text: "before-final"},
		{Kind: "final", Text: want},
		{Kind: "prompt", Text: "after-final"},
	}), 32)
	if len(pages) < 4 || pages[0].Content != "before-final" || pages[len(pages)-1].Content != "after-final" {
		t.Fatal("final does not have isolated continuation pages")
	}
	var got strings.Builder
	for i, page := range pages[1 : len(pages)-1] {
		if len(page.Content) > 3000 || !utf8.ValidString(page.Content) || len(page.Anchors) != 1 {
			t.Fatalf("invalid final page %d", i)
		}
		part := page.Content
		if strings.Contains(part, "| entry-") && !strings.Contains(part, header) {
			t.Fatalf("table continuation %d lost its header", i)
		}
		if i > 0 {
			part = strings.TrimPrefix(part, header)
		}
		got.WriteString(part)
	}
	if got.String() != want {
		t.Fatal("mixed final content changed beyond repeated headers")
	}
}

func TestTablePaginationFitsAtomicRowAtFullByteBudget(t *testing.T) {
	const header = "| key | value |\n| --- | --- |\n"
	row := "| fit | " + strings.Repeat("x", 3000-len(header)-len("| fit |  |\n")) + " |\n"
	input := header + row + "| next | intact |\n"
	parts := Split(input, 3000)
	if len(parts) != 2 || parts[0] != header+row || parts[1] != header+"| next | intact |\n" {
		t.Fatal("row that fits a full page was split or lost")
	}
}

func TestTablePaginationOversizedRowRetainsBoundedTextAndResumesTable(t *testing.T) {
	const header = "| key | value |\n| --- | --- |\n"
	before, after := "| before | intact |\n", "| after | intact |\n"
	large := "| huge | " + strings.Repeat("данные東京🙂", 700) + " |\n"
	for _, input := range []string{header + large, header + before + large + after} {
		parts := Split(input, 3000)
		var data strings.Builder
		for i, part := range parts {
			if len(part) > 3000 || !utf8.ValidString(part) {
				t.Fatalf("oversized row page %d is not bounded UTF-8", i)
			}
			data.WriteString(strings.ReplaceAll(part, header, ""))
			if strings.Contains(part, after) && !strings.Contains(part, header+after) {
				t.Fatal("normal rows after oversized row lost table structure")
			}
		}
		if data.String() != strings.TrimPrefix(input, header) {
			t.Fatal("oversized row fallback changed source row content")
		}
	}
}

func TestTablePaginationFencedLiteralIsNotRepeated(t *testing.T) {
	const header = "| literal | table |\n| --- | --- |\n"
	code := header + strings.Repeat("| example | данные東京🙂 |\n", 350)
	input := "```markdown\n" + code + "```"
	pages := Paginate(RenderBlocks([]Block{{Kind: "final", Text: input}}), 32)
	var got strings.Builder
	for i, page := range pages {
		if len(page.Content) > 3000 || !utf8.ValidString(page.Content) || !strings.HasPrefix(page.Content, "```markdown\n") || !strings.HasSuffix(page.Content, "```") {
			t.Fatalf("literal code page %d is not independently fenced and bounded", i)
		}
		body := strings.TrimSuffix(strings.TrimPrefix(page.Content, "```markdown\n"), "```")
		if i < len(pages)-1 {
			body = strings.TrimSuffix(body, "\n")
		}
		got.WriteString(body)
	}
	if got.String() != code || strings.Count(got.String(), header) != 1 {
		t.Fatal("literal fenced table changed or acquired repeated headers")
	}
}

func TestTablePaginationOversizedHeaderPreservesAllText(t *testing.T) {
	input := "| " + strings.Repeat("заголовок", 400) + " | value |\n| --- | --- |\n| key | data |"
	var got strings.Builder
	for _, part := range Split(input, 3000) {
		if len(part) > 3000 || !utf8.ValidString(part) {
			t.Fatal("oversized header fallback is not bounded UTF-8")
		}
		got.WriteString(part)
	}
	if got.String() != input {
		t.Fatal("oversized header fallback lost table content")
	}
}

func TestTablePaginationSkipsLiteralFenceVariants(t *testing.T) {
	const header = "| literal | table |\n| --- | --- |\n"
	for _, fence := range []string{"~~~", "````"} {
		t.Run(fence, func(t *testing.T) {
			inner := ""
			if fence == "````" {
				inner = "```\n"
			}
			input := fence + "markdown\n" + inner + header + strings.Repeat("| fixture | данные東京🙂 |\n", 350) + inner + fence
			pages := Paginate(RenderBlocks([]Block{{Kind: "final", Text: input}}), 32)
			var got strings.Builder
			for _, page := range pages {
				if len(page.Content) > 3000 || !utf8.ValidString(page.Content) {
					t.Fatal("literal page is not bounded UTF-8")
				}
				got.WriteString(page.Content)
			}
			if strings.Count(got.String(), header) != 1 || strings.Count(got.String(), "fixture") != 350 {
				t.Fatal("literal table acquired repeated headers or lost data")
			}
		})
	}
}

func TestTablePaginationColumnCountsRespectEscapedPipes(t *testing.T) {
	for _, test := range []struct {
		name, header string
		valid        bool
	}{
		{"mismatch", "| first | second | third |\n", false},
		{"escaped mismatch", "| first\\|second |\n", false},
		{"escaped valid", "| first\\|second | third |\n", true},
		{"escaped backslash", "| first\\\\|second | third |\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := test.header + "| --- | --- |\n"
			input := prefix + strings.Repeat("| fixture | данные東京🙂 |\n", 350)
			parts := Split(input, 3000)
			count := strings.Count(strings.Join(parts, ""), prefix)
			if (test.valid && count != len(parts)) || (!test.valid && count != 1) {
				t.Fatalf("header repetitions=%d pages=%d valid=%v", count, len(parts), test.valid)
			}
		})
	}
}

func TestTablePaginationSingleOversizedRowKeepsHeader(t *testing.T) {
	const prefix = "| original | header |\n| --- | --- |\n"
	input := prefix + "| huge | " + strings.Repeat("данные東京🙂", 700) + " |"
	if got := strings.Join(Split(input, 3000), ""); got != input {
		t.Fatal("single oversized row lost original header/separator or data")
	}
}

func TestTablePaginationLiteralThenRealTableKeepsMatchingFences(t *testing.T) {
	const literalHeader = "| literal | example |\n| --- | --- |\n"
	const realHeader = "| real | values |\n| --- | --- |\n"
	for _, marker := range []string{"~~~", "````"} {
		t.Run(marker, func(t *testing.T) {
			code := literalHeader + strings.Repeat("| literal-row | данные東京🙂 |\n", 350)
			input := marker + "markdown\n" + code + marker + "\n\n" + realHeader + strings.Repeat("| real-row | данные東京🙂 |\n", 350)
			pages := Paginate(RenderBlocks([]Block{{Kind: "final", Text: input}}), 32)
			var got strings.Builder
			for i, page := range pages {
				if len(page.Content) > 3000 || !utf8.ValidString(page.Content) {
					t.Fatalf("page %d is not bounded UTF-8", i)
				}
				if strings.Contains(page.Content, "literal-row") && (!strings.HasPrefix(page.Content, marker+"markdown\n") || !strings.Contains(page.Content, "\n"+marker)) {
					t.Fatalf("literal continuation %d lost matching %s fence", i, marker)
				}
				if strings.Contains(page.Content, "real-row") && !strings.Contains(page.Content, realHeader) {
					t.Fatalf("real table continuation %d lost header", i)
				}
				got.WriteString(page.Content)
			}
			if strings.Count(got.String(), literalHeader) != 1 || strings.Count(got.String(), "literal-row") != 350 || strings.Count(got.String(), "real-row") != 350 {
				t.Fatal("literal or real table content changed")
			}
		})
	}
}

func TestTablePaginationDynamicFenceBudget(t *testing.T) {
	for _, glyph := range []string{"`", "~"} {
		for _, width := range []int{3, 100, 1000} {
			t.Run(fmt.Sprintf("%s-%d", glyph, width), func(t *testing.T) {
				marker := strings.Repeat(glyph, width)
				opener := marker + strings.Repeat("language", 20) + "\n"
				body := strings.Repeat("x", 6000) + "\n"
				input := opener + body + marker
				parts := Split(input, 3000)
				var restored strings.Builder
				for i, part := range parts {
					if len(part) > 3000 || !strings.HasPrefix(part, opener) || !strings.HasSuffix(part, marker) {
						t.Fatalf("page %d bytes=%d opening=%v closing=%v", i, len(part), strings.HasPrefix(part, opener), strings.HasSuffix(part, marker))
					}
					data := strings.TrimSuffix(strings.TrimPrefix(part, opener), marker)
					if i < len(parts)-1 {
						data = strings.TrimSuffix(data, "\n")
					}
					restored.WriteString(data)
				}
				if restored.String() != body {
					t.Fatal("fenced body bytes changed")
				}
			})
		}
	}
}

func TestTablePaginationImpossibleFencePreservesBoundedBytes(t *testing.T) {
	for _, glyph := range []string{"`", "~"} {
		marker := strings.Repeat(glyph, 3000)
		input := marker + "language\n" + strings.Repeat("данные東京🙂", 350) + "\n" + marker
		var restored strings.Builder
		for _, part := range Split(input, 3000) {
			if len(part) > 3000 || !utf8.ValidString(part) {
				t.Fatalf("impossible fence produced invalid page: %d bytes", len(part))
			}
			restored.WriteString(part)
		}
		if restored.String() != input {
			t.Fatal("impossible fence fallback changed original bytes")
		}
	}
}

func TestTablePaginationUnclosedFenceKeepsBodyAndClosesEveryPage(t *testing.T) {
	opener := "~~~" + strings.Repeat("language", 20) + "\n"
	body := strings.Repeat("x", 6000)
	var restored strings.Builder
	for _, part := range Split(opener+body, 3000) {
		if len(part) > 3000 || !strings.HasPrefix(part, opener) || !strings.HasSuffix(part, "\n~~~") {
			t.Fatal("unclosed source fence produced an invalid continuation")
		}
		restored.WriteString(strings.TrimSuffix(strings.TrimPrefix(part, opener), "\n~~~"))
	}
	if restored.String() != body {
		t.Fatal("unclosed fence changed body bytes")
	}
}
