package notificationplan_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/notificationplan"
	"bria/internal/telegramrich"
)

func TestRichShortTableMatchesWholeMessageNormalization(t *testing.T) {
	const table = "| Name | Value |\n| --- | --- |\n| ёж | 🙂 |\n"
	for _, prefix := range []string{"Сессия x - результат\n", "Сессия x - результат\n\n", ""} {
		for _, leading := range []string{"", "\n", "\n\n", "\n\n\n"} {
			raw := leading + table
			want := telegramrich.NormalizeRichMarkdown(prefix + raw)
			pages, err := notificationplan.Rich(prefix, raw, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if len(pages) != 1 || pages[0] != want {
				t.Fatalf("prefix %q leading %q: got %q, want %q", prefix, leading, pages, want)
			}
		}
	}
}

func TestPlanJSONFieldNames(t *testing.T) {
	encoded, err := json.Marshal(notificationplan.Plan{Version: 2, InputHash: "digest", Pages: []string{"page"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"version":2,"input_hash":"digest","pages":["page"]}` {
		t.Fatalf("unexpected persisted schema: %s", encoded)
	}
}

func TestRichTableHeadersAndNormalizationGrowth(t *testing.T) {
	const header = "| Name | Value |\n| --- | --- |\n"
	var body strings.Builder
	body.WriteString(header)
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&body, "| row-%03d | %s |\n", i, strings.Repeat("ёж", 12))
	}
	pages, err := notificationplan.Rich("Сессия x - результат\n", body.String(), 256)
	if err != nil {
		t.Fatal(err)
	}
	assertFinal(t, pages, 256)
	joined := strings.Join(pages, "\n")
	for i := 0; i < 120; i++ {
		if strings.Count(joined, fmt.Sprintf("row-%03d", i)) != 1 {
			t.Fatalf("row %d missing or duplicated", i)
		}
	}
	for _, page := range pages {
		if strings.Contains(page, "row-") && !strings.Contains(page, "| <sub>Name</sub> | <sub>Value</sub> |\n| --- | --- |") {
			t.Fatal("continuation missing table header")
		}
	}
}

func TestRichCodeFencePreservesBody(t *testing.T) {
	body := strings.Repeat("print('🙂ёж')\n", 600)
	pages, err := notificationplan.Rich("Result\n", "```python\n"+body+"```", 240)
	if err != nil {
		t.Fatal(err)
	}
	assertFinal(t, pages, 240)
	var recovered strings.Builder
	for i, page := range pages {
		if !strings.HasPrefix(page, "Result\n```python\n") || !strings.HasSuffix(page, "```") {
			t.Fatal("code wrapper lost")
		}
		chunk := strings.TrimSuffix(strings.TrimPrefix(page, "Result\n```python\n"), "```")
		if i < len(pages)-1 {
			chunk = strings.TrimSuffix(chunk, "\n")
		}
		recovered.WriteString(chunk)
	}
	if recovered.String() != body {
		t.Fatal("code body changed")
	}
}

func TestRichOversizedWrappersFallback(t *testing.T) {
	for _, body := range []string{
		"<details><summary>" + strings.Repeat("я", 200) + "</summary>\n\n🙂body\n\n</details>",
		"```" + strings.Repeat("я", 200) + "\n🙂body\n```",
		strings.Repeat("🙂", 90),
	} {
		pages, err := notificationplan.Rich("P\n", body, 64)
		if err != nil {
			t.Fatal(err)
		}
		assertFinal(t, pages, 64)
		for i := range pages {
			pages[i] = strings.TrimPrefix(pages[i], "P\n")
		}
		if strings.Join(pages, "") != body {
			t.Fatal("fallback lost source text")
		}
	}
}

func TestRichRejectsImpossibleAndInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		prefix, body string
		limit        int
	}{
		{"", "x", 0}, {"prefix", "body", 6}, {"", "🙂", 3}, {"", "\xff", 80}, {"\xff", "x", 80},
	} {
		if pages, err := notificationplan.Rich(tc.prefix, tc.body, tc.limit); err == nil || pages != nil {
			t.Fatalf("expected atomic error for %+v", tc)
		}
	}
}

func TestRichFinalBudgetAndUTF8(t *testing.T) {
	prefix, body := "Готово\n", strings.Repeat("🙂ёж ", 1400)
	pages, err := notificationplan.Rich(prefix, body, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var recovered strings.Builder
	for _, page := range pages {
		if len(page) > 4096 || !utf8.ValidString(page) {
			t.Fatalf("invalid payload: bytes=%d", len(page))
		}
		if !strings.HasPrefix(page, prefix) {
			t.Fatal("missing prefix")
		}
		recovered.WriteString(strings.TrimPrefix(page, prefix))
	}
	if recovered.String() != body {
		t.Fatal("body changed")
	}
}

func assertFinal(t *testing.T, pages []string, limit int) {
	t.Helper()
	if len(pages) == 0 {
		t.Fatal("no pages")
	}
	for _, page := range pages {
		if len(page) > limit || !utf8.ValidString(page) {
			t.Fatalf("invalid final payload: %d bytes (limit %d)", len(page), limit)
		}
		if telegramrich.NormalizeRichMarkdown(page) != page {
			t.Fatal("payload normalization is not final")
		}
	}
}

func TestRichOversizedTableRetainsEveryCell(t *testing.T) {
	value := strings.Repeat("ж🙂", 1000)
	pages, err := notificationplan.Rich("P\n", "| Key | Value |\n| --- | --- |\n| unique-key | "+value+" |\n", 128)
	if err != nil {
		t.Fatal(err)
	}
	assertFinal(t, pages, 128)
	var recovered strings.Builder
	for _, page := range pages {
		recovered.WriteString(strings.TrimPrefix(page, "P\n"))
	}
	all := recovered.String()
	if strings.Count(all, "unique-key") != 1 || !strings.Contains(all, value) {
		t.Fatal("oversized row lost text")
	}
}

func TestRichFinalPrefixNormalizationBudget(t *testing.T) {
	// The prefix becomes a table header only when joined with the body.
	pages, err := notificationplan.Rich("| H |\n", "| --- |\n| "+strings.Repeat("x", 72)+" |\n", 120)
	if err != nil {
		t.Fatal(err)
	}
	assertFinal(t, pages, 120)
}

func TestRichSmallAndInvalidBudgets(t *testing.T) {
	for _, tc := range []struct {
		body  string
		limit int
	}{{"abc", 1}, {"ёж", 2}, {"🙂🙂", 4}, {"", 1}} {
		pages, err := notificationplan.Rich("", tc.body, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		assertFinal(t, pages, tc.limit)
		if strings.Join(pages, "") != tc.body {
			t.Fatal("small-budget body lost")
		}
	}
}

func TestRichConcurrentDeterminism(t *testing.T) {
	const prefix = "P\n"
	body := "| H |\n| --- |\n" + strings.Repeat("| 🙂 |\n", 1000)
	want, err := notificationplan.Rich(prefix, body, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			got, err := notificationplan.Rich(prefix, body, 4096)
			if err != nil {
				t.Fatal(err)
			}
			assertFinal(t, got, 4096)
			if !reflect.DeepEqual(got, want) {
				t.Fatal("plan changed across calls")
			}
			got[0] = "caller modification"
		})
	}
}
