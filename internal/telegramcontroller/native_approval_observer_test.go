package telegramcontroller

import (
	"context"
	"testing"

	"bria/internal/controllertelemetry"
	"bria/internal/nativeapprovalflow"
)

type approvalControllerObserver func(controllertelemetry.Event)

func (f approvalControllerObserver) ObserveControllerEvent(_ context.Context, event controllertelemetry.Event) {
	f(event)
}

func TestNativeApprovalObserverMapsSafeLifecycleOutcome(t *testing.T) {
	for _, test := range []struct {
		input nativeapprovalflow.Outcome
		want  controllertelemetry.Outcome
	}{
		{nativeapprovalflow.Confirmed, controllertelemetry.ApprovalConfirmed},
		{nativeapprovalflow.Stale, controllertelemetry.ApprovalStale},
		{nativeapprovalflow.Uncertain, controllertelemetry.ApprovalUncertain},
	} {
		t.Run(string(test.input), func(t *testing.T) {
			var got controllertelemetry.Event
			observer := nativeApprovalObserver{observer: approvalControllerObserver(func(event controllertelemetry.Event) { got = event })}
			observer.ObserveNativeApproval(context.Background(), nativeapprovalflow.Event{
				SessionID: "session", ProviderSessionID: "provider", Fingerprint: "fingerprint",
				Generation: 4, Outcome: test.input,
			})
			if got.Stage != controllertelemetry.NativeApproval || got.Outcome != test.want ||
				got.SessionID != "session" || got.ProviderSessionID != "provider" ||
				got.ApprovalFingerprint != "fingerprint" || got.Generation != 4 {
				t.Fatalf("mapped event=%+v", got)
			}
		})
	}
}
