package transcript

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestCodexCandidatesStopAtConfiguredLimit(t *testing.T) {
	layout := newTestLayout(t)
	for index := 0; index < 3; index++ {
		writeTestFile(t,
			filepath.Join(layout.codex, "2026", "08", fmt.Sprintf("%02d", index+1), fmt.Sprintf("rollout-%d.jsonl", index)),
			`{"type":"session_meta","payload":{"id":"session"}}`,
		)
	}
	reader := newTestReader(t, layout, func(config *Config) {
		config.MaxCodexFiles = 2
	})

	candidates, err := reader.codexCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(candidates))
	}
}
