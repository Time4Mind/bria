package telegramtrace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"bria/internal/safelog"
	"bria/internal/telegram"
	"bria/internal/telegramtrace"
	"bria/internal/telegramui"
)

type traceHTTPClientFunc func(*http.Request) (*http.Response, error)

func (function traceHTTPClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestRecordPersistsPrivateCorrelatedIdentities(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	observed := time.Now().Add(-time.Minute).Round(0)
	event := telegramtrace.Event{
		Stage: "callback.accept", OperationID: "private-operation", CallbackID: "private-button",
		SessionID: "private-session", ChatID: -987654321876, CarrierID: 76543219,
		ExpectedChatID: -987654321876, ExpectedCarrierID: 76543219,
		PresentationID: "private-presentation", ButtonIDs: []string{"private-other-button", "private-button"},
		Error:  "123456789:telegram-token-secret private-payload-marker /private/secret.txt",
		Reason: "presentation_replayed", Time: observed, Duration: 125 * time.Millisecond,
		Page: 0, Pages: 4, HasPage: true, FollowLatest: false, Retired: true, Result: "failed",
	}
	record := telegramtrace.Record(event, key, 7)
	if record.Class != safelog.Detailed || record.Type != "telegram.flow_stage" {
		t.Fatalf("record type/class = %q/%q", record.Type, record.Class)
	}
	dir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Write(record); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-", "987654321876", "76543219", "123456789", "telegram-token-secret", "/private/secret.txt"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("private value persisted: %q", forbidden)
		}
	}
	var got safelog.Event
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	refs := regexp.MustCompile(`^c_[0-9a-f]{64}$`)
	if !refs.MatchString(got.EntityID) {
		t.Errorf("operation ref invalid: %q", got.EntityID)
	}
	for _, field := range []string{"callback_ref", "card_ref", "expected_card_ref", "session_ref", "presentation_ref", "run_ref"} {
		if !refs.MatchString(got.Fields[field]) {
			t.Errorf("%s ref invalid: %q", field, got.Fields[field])
		}
	}
	buttons := strings.Split(got.Fields["button_refs"], ",")
	if len(buttons) != 2 || buttons[1] != got.Fields["callback_ref"] {
		t.Errorf("button/callback correlation lost: %#v", got.Fields)
	}
	if got.Fields["card_ref"] != got.Fields["expected_card_ref"] {
		t.Error("matching cards do not correlate")
	}
	for field, want := range map[string]string{"sequence": "7", "page": "0", "pages": "4", "follow_latest": "false", "retired": "true", "reason": "presentation_replayed", "stage": "callback.accept", "duration_ms": "125"} {
		if got.Fields[field] != want {
			t.Errorf("%s=%q, want %q", field, got.Fields[field], want)
		}
	}
	if got.ErrorCategory != "presentation_replayed" || got.Error != "[REDACTED]" {
		t.Errorf("unsafe or untyped error: %#v", got)
	}
	if !got.Time.Equal(observed) {
		t.Errorf("observation time lost: %s", got.Time)
	}
}

func TestInputReferenceIsSharedAcrossVoiceStages(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	operations := []string{"status:71", "telegram-update:71", "telegram-update:71:prompt-status:preprocessed"}
	want := ""
	for _, operation := range operations {
		got := telegramtrace.Record(telegramtrace.Event{OperationID: operation}, key, 1).Fields["input_ref"]
		if got == "" {
			t.Fatalf("input ref missing for %q", operation)
		}
		if want == "" {
			want = got
		} else if got != want {
			t.Fatalf("input refs differ: %q != %q", got, want)
		}
	}
	if got := telegramtrace.Record(telegramtrace.Event{OperationID: "telegram-update:72"}, key, 1).Fields["input_ref"]; got == want {
		t.Fatal("different Telegram input reused correlation")
	}
}

func TestRecordTargetIsIndependentOfRenderedPage(t *testing.T) {
	event := telegramtrace.Event{Action: telegramui.ActionMenuNodes, Target: 3, Page: 1, Pages: 5, HasPage: true}
	record := telegramtrace.Record(event, []byte("test-only-key"), 1)
	if record.Fields["target"] != "3" || record.Fields["page"] != "1" || record.Fields["pages"] != "5" {
		t.Errorf("clicked target/rendered page lost: %#v", record.Fields)
	}
	event.Target = 0
	if telegramtrace.Record(event, nil, 1).Fields["target"] != "0" {
		t.Error("zero target lost")
	}
	event.Target = -1
	if _, ok := telegramtrace.Record(event, nil, 1).Fields["target"]; ok {
		t.Error("negative target emitted")
	}
	event.Target, event.Action = 3, ""
	if _, ok := telegramtrace.Record(event, nil, 1).Fields["target"]; ok {
		t.Error("target emitted without action")
	}
}

func TestRecordDoesNotInventUnknownPageOrRetirementState(t *testing.T) {
	for _, test := range []struct {
		name            string
		event           telegramtrace.Event
		follow, retired string
	}{
		{"unknown", telegramtrace.Event{}, "", ""},
		{"known page", telegramtrace.Event{HasPage: true}, "false", ""},
		{"latest", telegramtrace.Event{HasPage: true, FollowLatest: true}, "true", ""},
		{"accepted without reason", telegramtrace.Event{Stage: "callback.accept", PresentationID: "private-presentation"}, "", ""},
		{"rejected without presentation", telegramtrace.Event{Stage: "callback.accept", Reason: "token_invalid"}, "", ""},
		{"rejected active", telegramtrace.Event{Stage: "callback.accept", Reason: "presentation_replayed", PresentationID: "private-presentation"}, "", "false"},
		{"discarded retired", telegramtrace.Event{Stage: "callback.discard", Reason: "presentation_replayed", PresentationID: "private-presentation", Retired: true}, "", "true"},
		{"unrelated stage", telegramtrace.Event{Stage: "card.commit", Reason: "card_commit_failed", PresentationID: "private-presentation", Retired: true}, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			fields := telegramtrace.Record(test.event, []byte("test-only-key"), 1).Fields
			for field, want := range map[string]string{"follow_latest": test.follow, "retired": test.retired} {
				got, exists := fields[field]
				if got != want || exists != (want != "") {
					t.Errorf("%s=%q, exists=%v; want %q", field, got, exists, want)
				}
			}
		})
	}
}

func TestRecordDomainsRunIsolationAndButtonBound(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	event := telegramtrace.Event{OperationID: "same", CallbackID: "same", SessionID: "same", PresentationID: "same", ChatID: 12, CarrierID: 3, ExpectedChatID: 1, ExpectedCarrierID: 23}
	for i := 0; i < 40; i++ {
		event.ButtonIDs = append(event.ButtonIDs, "same")
	}
	a := telegramtrace.Record(event, key, 1)
	// Independent OpenSSL HMAC-SHA256 fixture, key is 32 bytes of 0x2a.
	if a.EntityID != "c_ca847517fdb979872010805de72cd74e4bc7043dc65d2d7e989b8391b6411701" {
		t.Errorf("unexpected HMAC: %q", a.EntityID)
	}
	b := telegramtrace.Record(event, key, 2)
	otherRun := telegramtrace.Record(event, bytes.Repeat([]byte{43}, 32), 1)
	seen := map[string]bool{}
	for _, value := range []string{a.EntityID, a.Fields["callback_ref"], a.Fields["session_ref"], a.Fields["presentation_ref"], a.Fields["card_ref"], a.Fields["expected_card_ref"], a.Fields["run_ref"]} {
		if value == "" || seen[value] {
			t.Fatal("identity domains collided")
		}
		seen[value] = true
	}
	if a.EntityID != b.EntityID || a.Fields["run_ref"] != b.Fields["run_ref"] || a.Fields["run_ref"] == otherRun.Fields["run_ref"] || a.EntityID == otherRun.EntityID {
		t.Fatal("unstable or cross-run correlation")
	}
	for _, field := range []string{"callback_ref", "session_ref", "presentation_ref", "card_ref", "expected_card_ref"} {
		if a.Fields[field] != b.Fields[field] || a.Fields[field] == otherRun.Fields[field] {
			t.Errorf("bad run scoping for %s", field)
		}
	}
	buttons := strings.Split(a.Fields["button_refs"], ",")
	if len(buttons) != 32 {
		t.Fatalf("button bound = %d", len(buttons))
	}
	if a.Fields["button_refs_truncated"] != "8" {
		t.Errorf("missing truncation count: %q", a.Fields["button_refs_truncated"])
	}
	for _, button := range buttons {
		if button != a.Fields["callback_ref"] {
			t.Fatal("button correlation mismatch")
		}
	}
	empty := telegramtrace.Record(telegramtrace.Event{}, key, 0)
	for _, field := range []string{"callback_ref", "card_ref", "session_ref", "presentation_ref", "button_refs", "button_refs_truncated", "page", "pages", "reason"} {
		if _, ok := empty.Fields[field]; ok {
			t.Errorf("absent metadata emitted: %s", field)
		}
	}
}

func TestRecordUnknownReasonFailsClosedInPhysicalJSON(t *testing.T) {
	dir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []telegramtrace.Event{
		{Reason: "private_payload_marker"}, {Error: "secret_failure_text"},
		{Reason: "private_payload_marker", Error: "secret_failure_text"},
	} {
		record := telegramtrace.Record(event, []byte("test-only-key"), 1)
		if record.ErrorCategory != "operation_failed" || record.Fields["reason"] != "operation_failed" {
			t.Errorf("unknown reason did not fall back: %#v", record)
		}
		if err := logger.Write(record); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private_payload_marker")) || bytes.Contains(raw, []byte("secret_failure_text")) {
		t.Fatal("raw reason/error leaked")
	}
}

func TestTransportFailureClassReachesPhysicalFlowLogWithoutRawError(t *testing.T) {
	const privateDetail = "private-dns-detail"
	client, err := telegram.NewClient("123:test-secret", traceHTTPClientFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: privateDetail, Name: "private.invalid"}
	}), telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, requestErr := client.GetMe(context.Background())
	if requestErr == nil || !errors.Is(requestErr, telegram.ErrTransient) {
		t.Fatalf("GetMe error = %v, want transient transport failure", requestErr)
	}

	dir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	record := telegramtrace.Record(telegramtrace.Event{
		Stage: "transport.send", OperationID: "status:1", Result: "failed",
		Error: requestErr.Error(), Reason: telegramtrace.Reason(requestErr), Time: time.Now(),
	}, []byte("test-only-key"), 1)
	if err := logger.Write(record); err != nil {
		t.Fatal(err)
	}
	rows, err := logger.Read(safelog.Detailed)
	if err != nil || len(rows) != 1 || rows[0].ErrorCategory != "transport_dns" || rows[0].Fields["reason"] != "transport_dns" {
		t.Fatalf("physical flow rows = (%#v, %v), want transport_dns", rows, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{privateDetail, "private.invalid", "test-secret"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("physical flow log exposed private detail %q", forbidden)
		}
	}
}
