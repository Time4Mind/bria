package runtimehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/promptidentity"
)

type inputConfirmerStub struct {
	baseline    InputConfirmationBaseline
	baselineErr error
	confirmErr  error
	digest      string
	calls       int
}

func (s *inputConfirmerStub) BaselineInput(
	context.Context,
	RuntimeBinding,
) (InputConfirmationBaseline, error) {
	return s.baseline, s.baselineErr
}

func (s *inputConfirmerStub) ConfirmInput(
	_ context.Context,
	_ RuntimeBinding,
	_ InputConfirmationBaseline,
	digest string,
) error {
	s.calls++
	s.digest = digest
	return s.confirmErr
}

func TestInputConfirmationMatchesExactPromptWithoutResending(t *testing.T) {
	for _, test := range []struct {
		name          string
		legacy        bool
		confirmErr    error
		wantOutcome   string
		wantErr       error
		wantDelivered bool
		wantAccepted  string
	}{
		{name: "confirmed", wantOutcome: "confirmed", wantDelivered: true, wantAccepted: "true"},
		{name: "confirmed legacy binding", legacy: true, wantOutcome: "confirmed_legacy", wantDelivered: true, wantAccepted: "true"},
		{name: "provider busy", confirmErr: context.DeadlineExceeded, wantOutcome: "pending", wantDelivered: true, wantAccepted: "nil"},
		{name: "stale binding", confirmErr: ErrStaleRuntime, wantOutcome: "stale_generation", wantErr: ErrInputUnconfirmed, wantAccepted: "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := &fakeRuntimeDriver{}
			executor := newTestExecutor(t, driver)
			confirmer := &inputConfirmerStub{
				baseline: InputConfirmationBaseline{
					ProviderSessionID: "provider",
					LegacyGeneration:  test.legacy,
				},
				confirmErr: test.confirmErr,
			}
			executor.SetInputConfirmer(confirmer)
			timings := make(chan inputDeliveryTiming, 1)
			executor.inputTiming = func(timing inputDeliveryTiming) { timings <- timing }

			request := testRequest("long-input-confirmation", ActionSendInput)
			request.Text = "  длинный запрос\nс новой строкой  "
			result, executionErr := submitAndWaitInputResult(t, executor, request)
			if !errors.Is(executionErr, test.wantErr) {
				t.Fatalf("execution err=%v want=%v", executionErr, test.wantErr)
			}
			if result.Delivered != test.wantDelivered {
				t.Fatalf("result=%+v want delivered=%t", result, test.wantDelivered)
			}
			switch test.wantAccepted {
			case "nil":
				if result.ProviderAccepted != nil {
					t.Fatalf("result=%+v want pending confirmation", result)
				}
			case "true", "false":
				want := test.wantAccepted == "true"
				if result.ProviderAccepted == nil || *result.ProviderAccepted != want {
					t.Fatalf("result=%+v want accepted=%t", result, want)
				}
			}
			if confirmer.calls != 1 || confirmer.digest != promptidentity.Digest("длинный запрос\nс новой строкой") {
				t.Fatalf("confirmer calls=%d digest=%q", confirmer.calls, confirmer.digest)
			}
			if calls := driver.snapshot(); len(calls) != 1 || calls[0].action != "literal" {
				t.Fatalf("driver calls=%+v", calls)
			}
			if timing := <-timings; timing.confirmationOutcome != test.wantOutcome {
				t.Fatalf("timing=%+v", timing)
			}
			duplicate, err := executor.Submit(context.Background(), request)
			if err != nil || !duplicate.Duplicate || len(driver.snapshot()) != 1 {
				t.Fatalf("duplicate=%+v err=%v calls=%+v", duplicate, err, driver.snapshot())
			}
		})
	}
}

func TestInputConfirmationBaselineFailureLeavesSubmissionPending(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	confirmer := &inputConfirmerStub{baselineErr: errors.New("transcript unavailable")}
	executor.SetInputConfirmer(confirmer)
	timings := make(chan inputDeliveryTiming, 1)
	executor.inputTiming = func(timing inputDeliveryTiming) { timings <- timing }
	request := testRequest("baseline-unavailable", ActionSendInput)
	request.Text = "still submit"
	result, executionErr := submitAndWaitInputResult(t, executor, request)
	if executionErr != nil || !result.Delivered || result.ProviderAccepted != nil ||
		result.Detail != ProviderConfirmationPendingDetail {
		t.Fatalf("result=%+v err=%v", result, executionErr)
	}
	if confirmer.calls != 0 || len(driver.snapshot()) != 1 {
		t.Fatalf("confirm calls=%d driver=%+v", confirmer.calls, driver.snapshot())
	}
	if timing := <-timings; timing.confirmationOutcome != "baseline_unavailable" {
		t.Fatalf("timing=%+v", timing)
	}
}

func submitAndWaitInputResult(
	t *testing.T,
	executor *LocalExecutor,
	request Request,
) (Result, error) {
	t.Helper()
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result, found, err := executor.LookupResult(context.Background(), request.OperationID)
		if found {
			return result, err
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("input operation did not complete")
	return Result{}, nil
}
