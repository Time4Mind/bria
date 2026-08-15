package telegrambot

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestRichFallbackKeepsTechnicalBlocksExpandable(t *testing.T) {
	got := richFallbackMarkdownV2(telegramui.Screen{Text: strings.Join([]string{
		"Session header",
		"<details><summary>▷ Bash · ⏳ 0:04</summary>\n\n```bash\nnmap -sn 192.168.0.0/24\n```\n\n</details>",
		"agent commentary",
	}, "\n\n")})
	for _, want := range []string{
		"Session header",
		">▷ Bash · ⏳ 0:04\n>\\`\\`\\`bash\n>nmap \\-sn 192\\.168\\.0\\.0/24\n>\\`\\`\\`||",
		"agent commentary",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fallback missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "<details>") {
		t.Fatalf("Rich details leaked into MarkdownV2 fallback: %q", got)
	}
}

func TestOversizedRichFallbackKeepsSummariesAndDropsTechnicalBodies(t *testing.T) {
	got := richFallbackMarkdownV2(telegramui.Screen{Text: "<details><summary>✓ Bash</summary>\n\n" +
		strings.Repeat("_*[]()~`>#+-=|{}.!", 400) + "\n\n</details>",
	})
	if len(got) > MaxMessageTextBytes {
		t.Fatalf("fallback has %d bytes", len(got))
	}
	if !strings.Contains(got, "✓ Bash") || strings.Contains(got, "\\_\\*") {
		t.Fatalf("oversized fallback did not retain only the summary: %q", got)
	}
}
