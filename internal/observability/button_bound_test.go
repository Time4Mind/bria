package observability_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"bria/internal/observability"
	"bria/internal/safelog"
	"bria/internal/telegramtrace"
)

func TestTelegramFlowReportsBoundedButtonOmissions(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := observability.NewTelegramFlowObserver(logger)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 40)
	for i := range ids {
		ids[i] = "button-" + strconv.Itoa(i)
	}
	observer.ObserveTelegramFlow(context.Background(), telegramtrace.Event{Stage: "state.commit", ButtonIDs: ids})
	observer.Close()
	rows, err := logger.Read(safelog.Detailed)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].Fields["button_refs_truncated"] != "8" || len(strings.Split(rows[0].Fields["button_refs"], ",")) != 32 {
		t.Fatalf("bounded async queue lost truncation evidence: %v", rows[0].Fields)
	}
}
