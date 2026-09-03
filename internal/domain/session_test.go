package domain_test

import (
	"testing"
	"time"

	"bria/internal/domain"
)

func TestSessionLifecyclePersistsStatesTimesAndDeadline(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	session, err := domain.NewStartingSessionAt(
		"logical-1",
		"intent-1",
		"computer-1",
		domain.ProviderCodex,
		"/workspace/project",
		createdAt,
		domain.SessionLifetime12Hours,
	)
	if err != nil {
		t.Fatalf("NewStartingSessionAt() error = %v", err)
	}
	if got, want := session.Status(), domain.SessionStarting; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := session.CreatedAt(), createdAt; !got.Equal(want) {
		t.Fatalf("created at = %s, want %s", got, want)
	}
	if got, ok := session.Deadline(); !ok || !got.Equal(createdAt.Add(12*time.Hour)) {
		t.Fatalf("deadline = (%s, %t), want %s", got, ok, createdAt.Add(12*time.Hour))
	}

	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "codex-thread-1", Generation: 1}
	readyAt := createdAt.Add(time.Minute)
	session, err = session.ReadyAt(binding, readyAt)
	if err != nil {
		t.Fatalf("ReadyAt() error = %v", err)
	}
	runningAt := readyAt.Add(time.Minute)
	session, err = session.StartWork(runningAt)
	if err != nil {
		t.Fatalf("StartWork() error = %v", err)
	}
	if got, want := session.Status(), domain.SessionRunning; got != want {
		t.Fatalf("running status = %q, want %q", got, want)
	}
	closingAfterWorkAt := runningAt.Add(time.Minute)
	session, err = session.CloseAfterWork(closingAfterWorkAt)
	if err != nil {
		t.Fatalf("CloseAfterWork() error = %v", err)
	}
	if got, want := session.Status(), domain.SessionClosingAfterWork; got != want {
		t.Fatalf("closing-after-work status = %q, want %q", got, want)
	}
	closingAt := closingAfterWorkAt.Add(time.Minute)
	session, err = session.BeginClose(closingAt)
	if err != nil {
		t.Fatalf("BeginClose() error = %v", err)
	}
	archivedAt := closingAt.Add(time.Minute)
	session, err = session.Archive(archivedAt)
	if err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	if got, want := session.Status(), domain.SessionArchived; got != want {
		t.Fatalf("archived status = %q, want %q", got, want)
	}
	if got, want := session.StateChangedAt(), archivedAt; !got.Equal(want) {
		t.Fatalf("state changed at = %s, want %s", got, want)
	}

	resumingAt := archivedAt.Add(time.Hour)
	session, err = session.BeginResume(resumingAt)
	if err != nil {
		t.Fatalf("BeginResume() error = %v", err)
	}
	if got, want := session.Status(), domain.SessionResuming; got != want {
		t.Fatalf("resuming status = %q, want %q", got, want)
	}
	resumedAt := resumingAt.Add(time.Minute)
	resumedBinding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: binding.SessionID, Generation: 2}
	session, err = session.ResumeReady(resumedBinding, resumedAt, domain.SessionLifetime6Hours)
	if err != nil {
		t.Fatalf("ResumeReady() error = %v", err)
	}
	if got, ok := session.LastResumedAt(); !ok || !got.Equal(resumedAt) {
		t.Fatalf("last resumed at = (%s, %t), want %s", got, ok, resumedAt)
	}
	if got, ok := session.Deadline(); !ok || !got.Equal(resumedAt.Add(6*time.Hour)) {
		t.Fatalf("resumed deadline = (%s, %t), want %s", got, ok, resumedAt.Add(6*time.Hour))
	}
	if !session.Expired(resumedAt.Add(6 * time.Hour)) {
		t.Fatal("Expired() = false at deadline, want true")
	}

	restored, err := domain.RestoreSession(session.Snapshot())
	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	if !restored.Equal(session) {
		t.Fatalf("restored session = %#v, want exact lifecycle snapshot %#v", restored.Snapshot(), session.Snapshot())
	}
}

func TestAwaitRecoveryPreservesExactProviderBindingAndReturnState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 2, 11, 0, 0, 0, time.UTC)
	session, err := domain.NewStartingSessionAt("logical-2", "intent-2", "computer-1", domain.ProviderClaude, "/work", now, domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	prior := domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "claude-session-9", Generation: 8}
	session, err = session.ReadyAt(prior, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.StartWork(now.Add(2 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.AwaitRecoveryAt(now.Add(3 * time.Minute))
	if err != nil {
		t.Fatalf("AwaitRecoveryAt() error = %v", err)
	}
	if got, want := session.Status(), domain.SessionAwaitingRecovery; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, ok := session.Binding(); !ok || got != prior {
		t.Fatalf("binding = (%+v, %t), want exact prior %+v", got, ok, prior)
	}
	if got, ok := session.RecoveryTarget(); !ok || got != domain.SessionRunning {
		t.Fatalf("recovery target = (%q, %t), want %q", got, ok, domain.SessionRunning)
	}

	resumed := domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: prior.SessionID, Generation: prior.Generation + 1}
	session, err = session.Recovered(resumed, now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("Recovered() error = %v", err)
	}
	// The process generation that owned the turn has exited. Exact provider
	// identity is resumed, but Bria must not invent an in-flight turn in the new
	// adapter generation; durable message custody resolves that turn separately.
	if got, want := session.Status(), domain.SessionReady; got != want {
		t.Fatalf("recovered status = %q, want %q", got, want)
	}
}

func TestResumeReadyRejectsReplacementProviderSession(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	session, err := domain.NewStartingSessionAt("logical-3", "intent-3", "computer-1", domain.ProviderCodex, "/work", now, domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "original-thread", Generation: 3}
	session, _ = session.ReadyAt(prior, now.Add(time.Minute))
	session, _ = session.BeginClose(now.Add(2 * time.Minute))
	session, _ = session.Archive(now.Add(3 * time.Minute))
	session, _ = session.BeginResume(now.Add(4 * time.Minute))

	_, err = session.ResumeReady(domain.ProviderBinding{
		Provider: domain.ProviderCodex, SessionID: "replacement-thread", Generation: 4,
	}, now.Add(5*time.Minute), domain.SessionLifetimeNever)
	if err == nil {
		t.Fatal("ResumeReady() error = nil, want replacement-session rejection")
	}
}

func TestRecoveredInterruptedTurnReturnsReadyInsteadOfInventingRunningWork(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	for _, interrupted := range []domain.SessionStatus{domain.SessionRunning, domain.SessionStopping} {
		t.Run(string(interrupted), func(t *testing.T) {
			session, err := domain.NewStartingSessionAt("session", "intent", "local", domain.ProviderCodex, "/work", now, domain.SessionLifetimeNever)
			if err != nil {
				t.Fatal(err)
			}
			prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}
			session, _ = session.ReadyAt(prior, now.Add(time.Minute))
			session, _ = session.StartWork(now.Add(2 * time.Minute))
			if interrupted == domain.SessionStopping {
				session, _ = session.BeginStop(now.Add(3 * time.Minute))
			}
			awaiting, err := session.AwaitRecoveryAt(now.Add(4 * time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := awaiting.Recovered(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 2}, now.Add(5*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status() != domain.SessionReady {
				t.Fatalf("Recovered(%s) status = %q, want ready", interrupted, recovered.Status())
			}
		})
	}
}

func TestLifecycleTransitionsCoverStopAndResumeFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 2, 13, 0, 0, 0, time.UTC)
	session, err := domain.NewStartingSessionAt("logical-4", "intent-4", "computer-1", domain.ProviderClaude, "/work", now, domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	binding := domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "claude-original", Generation: 1}
	session, err = session.ReadyAt(binding, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.StartWork(now.Add(2 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.BeginStop(now.Add(3 * time.Minute))
	if err != nil || session.Status() != domain.SessionStopping {
		t.Fatalf("BeginStop() = (%q, %v), want stopping", session.Status(), err)
	}
	session, err = session.FinishWork(now.Add(4 * time.Minute))
	if err != nil || session.Status() != domain.SessionReady {
		t.Fatalf("FinishWork() = (%q, %v), want ready", session.Status(), err)
	}
	session, _ = session.BeginClose(now.Add(5 * time.Minute))
	session, _ = session.Archive(now.Add(6 * time.Minute))
	session, _ = session.BeginResume(now.Add(7 * time.Minute))
	session, err = session.FailResume(now.Add(8 * time.Minute))
	if err != nil || session.Status() != domain.SessionResumeFailed {
		t.Fatalf("FailResume() = (%q, %v), want resume_failed", session.Status(), err)
	}
	session, err = session.ReturnToArchive(now.Add(9 * time.Minute))
	if err != nil || session.Status() != domain.SessionArchived {
		t.Fatalf("ReturnToArchive() = (%q, %v), want archived", session.Status(), err)
	}
	if _, ok := session.Deadline(); ok {
		t.Fatal("never-lifetime archived session has a deadline")
	}
}
