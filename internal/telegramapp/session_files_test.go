package telegramapp

import (
	"slices"
	"testing"
)

func TestLocalFileCandidatesRecognizeAgentOutputWithoutTreatingOrdinaryTextAsPath(t *testing.T) {
	got := localFileCandidates("Готово: [отчёт](/workspace/out/report.pdf), также `images/chart.png`. Не файл: hello.")
	want := []string{"/workspace/out/report.pdf", "images/chart.png"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates=%q want=%q", got, want)
	}
}

func TestLocalFileCandidatesDeduplicateAndStripLineSuffix(t *testing.T) {
	got := localFileCandidates("`/workspace/report.md:12` and /workspace/report.md")
	if len(got) != 1 || got[0] != "/workspace/report.md" {
		t.Fatalf("candidates=%q", got)
	}
}
