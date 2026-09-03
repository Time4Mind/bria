package artifactretrycomposition_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/artifactdelivery"
	"bria/internal/artifactproduction"
	"bria/internal/artifactretrycomposition"
	"bria/internal/domain"
	"bria/internal/turnprocessing"
)

var testSessionID = domain.SessionID("123e4567-e89b-12d3-a456-426614174000")

type deliveryStub struct {
	deliveries int
	retries    int
	initial    artifactproduction.Result
	retry      artifactproduction.Result
	retryErr   error
	recovered  []artifactproduction.RecoveredRetry
	finalID    string
}

func (stub *deliveryStub) DeliverFinal(_ context.Context, final turnprocessing.FinalObservation) (artifactproduction.Result, error) {
	stub.deliveries++
	stub.finalID = final.OperationID
	result := stub.initial
	result.Summary.FinalID = final.OperationID
	return result, nil
}

func (stub *deliveryStub) Retry(context.Context, string) (artifactproduction.Result, error) {
	stub.retries++
	result := stub.retry
	result.Summary.FinalID = stub.finalID
	return result, stub.retryErr
}

func (stub *deliveryStub) RecoverClaimedResults(context.Context) ([]artifactproduction.RecoveredRetry, error) {
	return stub.recovered, nil
}

type publisherStub struct {
	notices []artifactretrycomposition.Notice
	err     error
}

func (stub *publisherStub) PublishArtifactRetry(_ context.Context, notice artifactretrycomposition.Notice) error {
	stub.notices = append(stub.notices, notice)
	return stub.err
}

func TestFinalProcessorPersistsOneExactManualDecisionAcrossRestart(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	delivery := &deliveryStub{initial: pendingResult("descriptor-1", now.Add(time.Hour))}
	publisher := &publisherStub{}
	path := filepath.Join(t.TempDir(), "artifact-retries.json")
	composition, err := artifactretrycomposition.Open(path, delivery, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := composition.BindPublisher(publisher); err != nil {
		t.Fatal(err)
	}
	final := turnprocessing.FinalObservation{
		SessionID: testSessionID, MessageID: "telegram-update:41", OperationID: "telegram-update:41:final", Text: "final",
	}
	if err := composition.ProcessFinal(context.Background(), final); err != nil {
		t.Fatalf("ProcessFinal() error = %v", err)
	}
	if delivery.deliveries != 1 || len(publisher.notices) != 1 {
		t.Fatalf("deliveries/notices = %d/%d, want 1/1", delivery.deliveries, len(publisher.notices))
	}
	notice := publisher.notices[0]
	if notice.OperationID == "" || notice.Sequence != 41 || notice.Binding.SessionID != testSessionID || notice.Binding.MessageID != final.MessageID ||
		notice.Binding.FinalOperationID != final.OperationID || notice.Binding.Generation != 1 || notice.Binding.Slot != 1 ||
		notice.Binding.PresentationID == "" || notice.Summary.Total != 2 || notice.Summary.Confirmed != 1 || notice.Summary.Unconfirmed != 1 {
		t.Fatalf("notice = %#v", notice)
	}
	if notice.Binding.PresentationID == testSessionID {
		t.Fatal("presentation slot leaked the logical session identity instead of using an isolated capability slot")
	}

	reopenedDelivery := &deliveryStub{initial: pendingResult("descriptor-1", now.Add(time.Hour))}
	reopenedPublisher := &publisherStub{}
	reopened, err := artifactretrycomposition.Open(path, reopenedDelivery, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.BindPublisher(reopenedPublisher); err != nil {
		t.Fatal(err)
	}
	if err := reopened.ProcessFinal(context.Background(), final); err != nil {
		t.Fatalf("reopened ProcessFinal() error = %v", err)
	}
	if reopenedDelivery.deliveries != 0 || len(reopenedPublisher.notices) != 0 {
		t.Fatalf("restart auto activity = deliveries %d, notices %d", reopenedDelivery.deliveries, len(reopenedPublisher.notices))
	}
}

func TestManualRetryIsExactOneShotAndUnknownRotatesDecision(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	delivery := &deliveryStub{
		initial: pendingResult("descriptor-1", now.Add(time.Hour)),
		retry:   pendingResult("descriptor-2", now.Add(2*time.Hour)),
	}
	publisher := &publisherStub{}
	composition, err := artifactretrycomposition.Open(filepath.Join(t.TempDir(), "retry.json"), delivery, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := composition.BindPublisher(publisher); err != nil {
		t.Fatal(err)
	}
	final := turnprocessing.FinalObservation{SessionID: testSessionID, MessageID: "telegram-update:42", OperationID: "telegram-update:42:final", Text: "final"}
	if err := composition.ProcessFinal(context.Background(), final); err != nil {
		t.Fatal(err)
	}
	first := publisher.notices[0].Binding
	outcome, err := composition.Retry(context.Background(), "status:900", first)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if delivery.retries != 1 || outcome.Next == nil || outcome.Next.Generation != 2 || outcome.Next.Slot != 1 ||
		outcome.Next.PresentationID == first.PresentationID || outcome.Summary.Unconfirmed != 1 {
		t.Fatalf("retry outcome/calls = %#v/%d", outcome, delivery.retries)
	}
	if _, err := composition.Retry(context.Background(), "status:901", first); !errors.Is(err, artifactretrycomposition.ErrStaleDecision) {
		t.Fatalf("second click error = %v, want ErrStaleDecision", err)
	}
	tampered := *outcome.Next
	tampered.MessageID = "telegram-update:other"
	if _, err := composition.Retry(context.Background(), "status:902", tampered); !errors.Is(err, artifactretrycomposition.ErrStaleDecision) {
		t.Fatalf("tampered binding error = %v, want ErrStaleDecision", err)
	}
	if delivery.retries != 1 {
		t.Fatalf("stale/tampered decisions retried %d times", delivery.retries)
	}
}

func TestFinalObservationAndExpiredDecisionFailClosed(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	delivery := &deliveryStub{initial: pendingResult("descriptor", now.Add(time.Minute))}
	publisher := &publisherStub{}
	composition, err := artifactretrycomposition.Open(filepath.Join(t.TempDir(), "retry.json"), delivery, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := composition.BindPublisher(publisher); err != nil {
		t.Fatal(err)
	}
	bad := turnprocessing.FinalObservation{SessionID: testSessionID, MessageID: "telegram-update:43", OperationID: "telegram-update:44:final", Text: "final"}
	if err := composition.ProcessFinal(context.Background(), bad); !errors.Is(err, artifactretrycomposition.ErrInvalidObservation) {
		t.Fatalf("mismatched final error = %v", err)
	}
	good := turnprocessing.FinalObservation{SessionID: testSessionID, MessageID: "telegram-update:43", OperationID: "telegram-update:43:final", Text: "final"}
	if err := composition.ProcessFinal(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := composition.Retry(context.Background(), "status:903", publisher.notices[0].Binding); !errors.Is(err, artifactretrycomposition.ErrStaleDecision) {
		t.Fatalf("expired decision error = %v, want ErrStaleDecision", err)
	}
	if delivery.retries != 0 {
		t.Fatalf("expired decision retried %d times", delivery.retries)
	}
}

func TestReopenReconcilesInFlightClaimToNewManualGenerationWithoutSend(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "retry.json")
	firstDelivery := &deliveryStub{initial: pendingResult("descriptor-1", now.Add(time.Hour)), retryErr: errors.New("crash boundary")}
	first, err := artifactretrycomposition.Open(path, firstDelivery, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	publisher := &publisherStub{}
	_ = first.BindPublisher(publisher)
	final := turnprocessing.FinalObservation{SessionID: testSessionID, MessageID: "telegram-update:44", OperationID: "telegram-update:44:final", Text: "final"}
	if err := first.ProcessFinal(context.Background(), final); err != nil {
		t.Fatal(err)
	}
	old := publisher.notices[0].Binding
	if _, err := first.Retry(context.Background(), "status:904", old); err == nil {
		t.Fatal("simulated interrupted retry unexpectedly succeeded")
	}

	recovery := pendingResult("descriptor-2", now.Add(2*time.Hour))
	recovery.Summary.FinalID = final.OperationID
	reopenedDelivery := &deliveryStub{recovered: []artifactproduction.RecoveredRetry{{FinalID: final.OperationID, Summary: recovery.Summary, Retry: recovery.Retry}}}
	reopened, err := artifactretrycomposition.Open(path, reopenedDelivery, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := reopened.Retry(context.Background(), "status:904", old)
	if err != nil || outcome.Next == nil || outcome.Next.Generation != old.Generation+1 {
		t.Fatalf("reconciled exact callback = %#v, %v", outcome, err)
	}
	if reopenedDelivery.retries != 0 {
		t.Fatalf("reconcile auto-retried artifact %d times", reopenedDelivery.retries)
	}
	if _, err := reopened.Retry(context.Background(), "status:905", old); !errors.Is(err, artifactretrycomposition.ErrStaleDecision) {
		t.Fatalf("different replay error = %v", err)
	}
}

func TestFailedSummaryEnqueueIsRedrivenOnBindWithoutArtifactRetry(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "retry.json")
	delivery := &deliveryStub{initial: pendingResult("descriptor-1", now.Add(time.Hour))}
	composition, err := artifactretrycomposition.Open(path, delivery, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	failing := &publisherStub{err: errors.New("crash before durable enqueue")}
	if err := composition.BindPublisher(failing); err != nil {
		t.Fatal(err)
	}
	final := turnprocessing.FinalObservation{SessionID: testSessionID, MessageID: "telegram-update:45", OperationID: "telegram-update:45:final", Text: "final"}
	if err := composition.ProcessFinal(context.Background(), final); err == nil {
		t.Fatal("ProcessFinal() unexpectedly accepted a failed summary enqueue")
	}
	if delivery.deliveries != 1 || len(failing.notices) != 1 {
		t.Fatalf("initial deliveries/notices = %d/%d, want 1/1", delivery.deliveries, len(failing.notices))
	}

	reopenedDelivery := &deliveryStub{}
	reopened, err := artifactretrycomposition.Open(path, reopenedDelivery, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	recovered := &publisherStub{}
	if err := reopened.BindPublisher(recovered); err != nil {
		t.Fatalf("BindPublisher() recovery error = %v", err)
	}
	if len(recovered.notices) != 1 || recovered.notices[0].OperationID != failing.notices[0].OperationID {
		t.Fatalf("recovered notices = %#v, want one stable operation", recovered.notices)
	}
	if reopenedDelivery.deliveries != 0 || reopenedDelivery.retries != 0 {
		t.Fatalf("summary recovery sent artifacts: deliveries/retries = %d/%d", reopenedDelivery.deliveries, reopenedDelivery.retries)
	}
}

func pendingResult(token string, expires time.Time) artifactproduction.Result {
	return artifactproduction.Result{
		Summary: artifactdelivery.Summary{Total: 2, Confirmed: 1, Unconfirmed: 1, NeedsExplicitRetry: true},
		Retry:   &artifactproduction.RetryDescriptor{Token: token, ExpiresAt: expires},
	}
}
