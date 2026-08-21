package telegramapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
)

func TestRestoreTimingLogsCorrelatedBoundedStages(t *testing.T) {
	root := t.TempDir()
	manager, err := processlog.Start(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	(&restoreCallbackTiming{
		startedAt: time.Now().Add(-2 * time.Second), ref: ref, generation: 7,
		outcome: "initial_ready", resolve: time.Millisecond, ack: 2 * time.Millisecond,
		control: 3 * time.Millisecond, render: 4 * time.Millisecond, edit: 5 * time.Millisecond,
	}).log()
	(&restoreReadyTiming{
		startedAt: time.Now().Add(-3 * time.Second), ref: ref, generation: 7,
		observedGeneration: 7, outcome: "ready", wait: time.Second, render: 6 * time.Millisecond,
		edit: 7 * time.Millisecond,
	}).log()
	tagged := withRestoreTiming(context.Background(), ref, 7, "settled")
	(sessionCardTiming{
		startedAt: time.Now().Add(-30 * time.Millisecond), outcome: "ok",
		pane: paneAttachTiming{outcome: "skipped"},
	}).log(tagged, ref, 0)
	logSlowTelegramOperationContext(
		tagged, "edit_screen_text", 42, time.Now().Add(-30*time.Millisecond), nil,
	)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	detail := readRestoreTimingLogs(t, root, "detail-")
	service := readRestoreTimingLogs(t, root, "service-")
	for _, logText := range []string{detail, service} {
		for _, expected := range []string{
			`stage=callback ref="node/session" generation=7 outcome=initial_ready`,
			"resolve_ms=1 callback_ack_ms=2 control_ms=3 initial_render_ms=4 initial_edit_ms=5",
			`stage=ready ref="node/session" generation=7 observed_generation=7 outcome=ready`,
			"settle_wait_ms=1000 settled_render_ms=6 settled_edit_ms=7",
			"slow_restore=true",
		} {
			if !strings.Contains(logText, expected) {
				t.Fatalf("restore timing log does not contain %q: %s", expected, logText)
			}
		}
	}
	for _, expected := range []string{
		"card_timing", "restore_stage=settled restore_generation=7",
		`outbound_timing operation=edit_screen_text message_id=42`,
		`restore_ref="node/session" restore_generation=7 restore_stage=settled`,
	} {
		if !strings.Contains(detail, expected) {
			t.Fatalf("correlated detail log does not contain %q: %s", expected, detail)
		}
	}
}

func TestRestoreSettlementOutcomeDistinguishesFailureAndSupersession(t *testing.T) {
	tests := []struct {
		name        string
		session     domain.Session
		expected    uint64
		settled     bool
		wantOutcome string
	}{
		{
			name: "waiting", expected: 7, settled: false, wantOutcome: "waiting",
			session: domain.Session{
				State: domain.SessionLive, RuntimeGeneration: 7, ResumePending: true,
			},
		},
		{
			name: "ready", expected: 7, settled: true, wantOutcome: "ready",
			session: domain.Session{State: domain.SessionLive, RuntimeGeneration: 7},
		},
		{
			name: "resume failed", expected: 7, settled: true, wantOutcome: "resume_failed",
			session: domain.Session{
				State: domain.SessionArchived, RuntimeGeneration: 7,
				ArchiveReason: domain.ArchiveResumeFailed,
			},
		},
		{
			name: "superseded", expected: 7, settled: true, wantOutcome: "superseded",
			session: domain.Session{
				State: domain.SessionLive, RuntimeGeneration: 8, ResumePending: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settled, outcome := restoreSettlementOutcome(test.session, test.expected)
			if settled != test.settled || outcome != test.wantOutcome {
				t.Fatalf("settled=%v outcome=%q", settled, outcome)
			}
		})
	}
}

func readRestoreTimingLogs(t *testing.T, root, prefix string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var result strings.Builder
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		result.Write(data)
	}
	return result.String()
}
