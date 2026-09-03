package observabilitycomposition_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/observability"
	"bria/internal/observabilitycomposition"
	"bria/internal/safelog"
	"bria/internal/sessionruntime"
	"bria/internal/turnprocessing"
)

const testSessionID = domain.SessionID("11111111-1111-4111-8111-111111111111")

type runtime struct {
	accepted []string
	events   []sessionruntime.TurnEvent
	result   sessionruntime.TurnResult
	err      error
}

func (r *runtime) Submit(_ context.Context, _ domain.SessionID, text string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{Final: text}, r.err
}
func (r *runtime) SubmitWithCallbacks(_ context.Context, _ domain.SessionID, _ string, c sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	for _, accepted := range r.accepted {
		if c.OnAccepted != nil {
			if err := c.OnAccepted(accepted); err != nil {
				return sessionruntime.TurnResult{}, err
			}
		}
	}
	for _, event := range r.events {
		if c.OnEvent != nil {
			if err := c.OnEvent(event); err != nil {
				return sessionruntime.TurnResult{}, err
			}
		}
	}
	return r.result, r.err
}

type preparedRuntime struct{ *runtime }

func (r *preparedRuntime) SubmitPreparedWithCallbacks(ctx context.Context, id domain.SessionID, in turnprocessing.PreparedInput, c sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	return r.SubmitWithCallbacks(ctx, id, in.Text, c)
}

type resolver struct {
	provider domain.Provider
	err      error
}

func (r resolver) ProviderForSession(context.Context, domain.SessionID) (domain.Provider, error) {
	return r.provider, r.err
}

var codex = resolver{provider: domain.ProviderCodex}

func recorder(t *testing.T) (*observability.Recorder, *safelog.Logger, string) {
	t.Helper()
	dir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observability.New(logger)
	if err != nil {
		t.Fatal(err)
	}
	return recorder, logger, dir
}

func TestSubmitWithCallbacksPreservesCallbacksAndRecordsFirstTimings(t *testing.T) {
	recorder, logger, _ := recorder(t)
	base := &runtime{accepted: []string{"a", "b"}, events: []sessionruntime.TurnEvent{{Kind: "commentary", Text: "private final"}, {Kind: "commentary", Text: "again"}}, result: sessionruntime.TurnResult{Final: "do not log"}}
	submitter, err := observabilitycomposition.New(base, recorder, codex)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	callbacks := sessionruntime.TurnCallbacks{MessageID: "occurrence-42", OnAccepted: func(value string) error {
		got = append(got, "accepted:"+value)
		if value == "a" {
			time.Sleep(30 * time.Millisecond)
		}
		return nil
	}, OnEvent: func(event sessionruntime.TurnEvent) error {
		got = append(got, "event:"+event.Text)
		if event.Text == "private final" {
			time.Sleep(30 * time.Millisecond)
		}
		return nil
	}}
	result, err := submitter.SubmitWithCallbacks(context.Background(), testSessionID, "secret prompt", callbacks)
	if err != nil || result.Final != "do not log" {
		t.Fatalf("SubmitWithCallbacks() = %#v, %v", result, err)
	}
	if want := []string{"accepted:a", "accepted:b", "event:private final", "event:again"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("callbacks = %#v, want %#v", got, want)
	}
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 1 {
		t.Fatalf("Read() = %#v, %v", events, err)
	}
	event := events[0]
	if event.Result != "success" || event.ErrorCategory != "" || event.Fields["provider_accept_ms"] == "" || event.Fields["first_event_ms"] == "" || event.Fields["total_ms"] == "" {
		t.Fatalf("event = %#v", event)
	}
	accept, _ := strconv.Atoi(event.Fields["provider_accept_ms"])
	first, _ := strconv.Atoi(event.Fields["first_event_ms"])
	if accept > 15 || first > 45 {
		t.Fatalf("first measurements overwritten: %#v", event.Fields)
	}
	expectedScope, _ := observability.NewScope(string(testSessionID))
	expectedID, _ := expectedScope.Correlation("provider.codex.submit", callbacks.MessageID)
	if event.EntityID != expectedID || event.Fields["operation"] != "provider.codex.submit" {
		t.Fatalf("event correlation = %#v", event)
	}
	serialized := event.EntityID + event.Fields["operation"] + event.Fields["provider_accept_ms"]
	for _, raw := range []string{string(testSessionID), callbacks.MessageID, "secret prompt", result.Final, base.events[0].Text} {
		if strings.Contains(serialized, raw) {
			t.Fatalf("raw value %q was persisted in %#v", raw, event)
		}
	}
}

func TestSubmitWithCallbacksRecordsFailureAndCancellationWithoutChangingError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		category string
	}{{"failure", errors.New("provider private failure"), "provider_error"}, {"cancel", context.Canceled, "cancelled"}, {"callback", errors.New("callback private failure"), "provider_error"}} {
		t.Run(tc.name, func(t *testing.T) {
			recorder, logger, _ := recorder(t)
			base := &runtime{err: tc.err}
			submitter, err := observabilitycomposition.New(base, recorder, codex)
			if err != nil {
				t.Fatal(err)
			}
			callbacks := sessionruntime.TurnCallbacks{MessageID: "m-1"}
			if tc.name == "callback" {
				base.err = nil
				base.accepted = []string{"accepted"}
				callbacks.OnAccepted = func(string) error { return tc.err }
			}
			_, got := submitter.SubmitWithCallbacks(context.Background(), testSessionID, "secret", callbacks)
			if !errors.Is(got, tc.err) {
				t.Fatalf("error = %v, want %v", got, tc.err)
			}
			events, err := logger.Read(safelog.Service)
			if err != nil || len(events) != 1 || events[0].Result != "error" || events[0].ErrorCategory != tc.category {
				t.Fatalf("events = %#v, %v", events, err)
			}
		})
	}
}

func TestLoggingFailureAndInvalidScopeDoNotChangeSubmission(t *testing.T) {
	recorder, logger, dir := recorder(t)
	base := &runtime{result: sessionruntime.TurnResult{Final: "ok"}}
	submitter, err := observabilitycomposition.New(base, recorder, codex)
	if err != nil {
		t.Fatal(err)
	}
	unresolved, err := observabilitycomposition.New(base, recorder, resolver{err: errors.New("binding unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := unresolved.SubmitWithCallbacks(context.Background(), testSessionID, "secret", sessionruntime.TurnCallbacks{MessageID: "m-0"}); err != nil || result.Final != "ok" {
		t.Fatalf("resolver failure changed result: %#v, %v", result, err)
	}
	if events, err := logger.Read(safelog.Service); err != nil || len(events) != 0 {
		t.Fatalf("resolver failure logged %#v, %v", events, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if result, err := submitter.SubmitWithCallbacks(context.Background(), testSessionID, "secret", sessionruntime.TurnCallbacks{MessageID: "m-1"}); err != nil || result.Final != "ok" {
		t.Fatalf("logging failure changed result: %#v, %v", result, err)
	}
	if result, err := submitter.SubmitWithCallbacks(context.Background(), "not-a-uuid", "secret", sessionruntime.TurnCallbacks{MessageID: "m-1"}); err != nil || result.Final != "ok" {
		t.Fatalf("invalid scope changed result: %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "service.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected log after failed logger: %v", err)
	}
}

func TestPreparedWrapperIsExplicitAndPlainSubmitDoesNotLog(t *testing.T) {
	recorder, logger, _ := recorder(t)
	base := &runtime{result: sessionruntime.TurnResult{Final: "ok"}}
	submitter, err := observabilitycomposition.New(base, recorder, codex)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := submitter.Submit(context.Background(), testSessionID, "plain secret"); err != nil {
		t.Fatal(err)
	}
	if events, err := logger.Read(safelog.Service); err != nil || len(events) != 0 {
		t.Fatalf("plain Submit logged %#v, %v", events, err)
	}
	if _, ok := any(submitter).(turnprocessing.PreparedTurnSubmitter); ok {
		t.Fatal("ordinary wrapper falsely claims prepared capability")
	}
	prepared, err := observabilitycomposition.NewPrepared(&preparedRuntime{runtime: base}, recorder, resolver{provider: domain.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.SubmitPreparedWithCallbacks(context.Background(), testSessionID, turnprocessing.PreparedInput{Text: "secret"}, sessionruntime.TurnCallbacks{MessageID: "m-2"}); err != nil {
		t.Fatal(err)
	}
	if events, err := logger.Read(safelog.Service); err != nil || len(events) != 1 || events[0].Fields["operation"] != "provider.claude.submit" {
		t.Fatalf("prepared event %#v, %v", events, err)
	}
}
