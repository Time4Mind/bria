package telegramflow_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestIndependentTechnicalLimitsSignedCallbackReopenRichWire(t *testing.T) {
	for _, tc := range []struct {
		commandClicks, outputClicks int
		commands, output            int
	}{
		{0, 1, 10, 20},
		{2, 2, 3, 3},
		{2, 3, 3, 5},
		{3, 2, 5, 3},
		{4, 2, 10, 3},
		{1, 2, 20, 3},
	} {
		for _, outputFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d+%d/outputFirst=%t", tc.commands, tc.output, outputFirst), func(t *testing.T) {
				exerciseTechnicalSettingsWire(t, tc.outputClicks, tc.output, tc.commandClicks, tc.commands, "", outputFirst)
			})
		}
	}
}

func TestLegacyTechnicalLimitsSignedCallbackReopenRichWire(t *testing.T) {
	for version := 1; version <= 5; version++ {
		for _, tc := range []struct {
			old, commands, nextOutput int
		}{{0, 10, 20}, {5, 5, 10}, {10, 10, 20}, {20, 20, 3}, {40, 20, 3}} {
			t.Run(fmt.Sprintf("v%d/shared%d", version, tc.old), func(t *testing.T) {
				// Literal old schema: no command field, and old 40 is accepted only
				// as a migration input. An actual output callback persists both.
				document := fmt.Sprintf(`{"version":%d,"revision":7,
"continue_existing":true,"screen_enabled":false,"card_detail":"standard",
"show_technical_actions":true,"notify_background_questions":false,
"notify_background_errors":true,"session_lifetime":"12h","queue_limit":8,
"voice_recognition":"parakeet","retry_undelivered_files":false,
"card_page_limit":64,"archive_recommendations":false,"default_providers":{},
"default_workdirs":{},"preprocessing_enabled":false,"preprocessing_instruction":"",
"session_naming_enabled":false`, version)
				if tc.old != 0 {
					document += fmt.Sprintf(`,"technical_output_lines":%d`, tc.old)
				}
				exerciseTechnicalSettingsWire(t, 1, tc.nextOutput, 0, tc.commands, document+"}", false)
			})
		}
	}
}

func assertIndependentTechnicalWire(t *testing.T, markdown string, commands, output int) {
	t.Helper()
	// Expected rows are literal fixture identities, not renderer output. The
	// separator and notices cannot take any of either content budget.
	for _, group := range []struct {
		prefix string
		limit  int
	}{{"CMD", commands}, {"ROW", output}} {
		var rows []string
		for i := 1; i <= group.limit; i++ {
			row := fmt.Sprintf("%s%02d", group.prefix, i)
			rows = append(rows, row)
			if strings.Count(markdown, row) != 1 {
				t.Fatalf("expected exactly one %s in physical Rich wire: %s", row, markdown)
			}
		}
		if strings.Count(markdown, group.prefix) != group.limit || !strings.Contains(markdown, strings.Join(rows, "\n")) || strings.Contains(markdown, fmt.Sprintf("%s%02d", group.prefix, group.limit+1)) {
			t.Fatalf("independent %s budget=%d not respected: %s", group.prefix, group.limit, markdown)
		}
	}
	if strings.Count(markdown, "<details>") != 1 || !strings.Contains(markdown, "\n\n---\n\n") || !strings.Contains(markdown, "… (truncated)") {
		t.Fatalf("paired tool lost spoiler/separator/truncation notice: %s", markdown)
	}
}
