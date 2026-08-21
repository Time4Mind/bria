package processlog

import "testing"

func TestFailureClassForOutcomeIsExplicitAndFailsClosed(t *testing.T) {
	tests := map[string]FailureClass{
		"ok":                            FailureNone,
		"retry_scheduled":               FailureTransient,
		"foreground_timeout":            FailureTimeout,
		"rate_limited":                  FailureRateLimited,
		"runtime_unavailable":           FailureAvailability,
		"stale_generation":              FailureStaleState,
		"validation_failed":             FailureInvalidState,
		"archive_verify_failed":         FailureIO,
		"raft_complete_failed":          FailureConsistency,
		"initial_edit_failed":           FailureTransport,
		"transcript_error":              FailureInternal,
		"runtime_failure_commit_failed": FailureConsistency,
		"watchdog_handoff":              FailureAvailability,
		"new_unreviewed_outcome":        FailureUnclassified,
	}
	for outcome, expected := range tests {
		if actual := FailureClassForOutcome(outcome); actual != expected {
			t.Errorf("outcome=%q class=%q want=%q", outcome, actual, expected)
		}
	}
}

func TestLegacySeverityDefaultsNeverInferFailureFromMessageText(t *testing.T) {
	if class := classifyFailure(Service, "outcome=failed error=secret"); class != FailureNone {
		t.Fatalf("service class=%q", class)
	}
	if class := classifyFailure(Critical, "everything looks fine"); class != FailureUnclassified {
		t.Fatalf("critical class=%q", class)
	}
	if class := normalizeFailureClass(FailureClass("invented")); class != FailureUnclassified {
		t.Fatalf("unknown explicit class=%q", class)
	}
}
