package callbackdiagnostic_test

import (
	"errors"
	"fmt"
	"testing"

	"bria/internal/callbackdiagnostic"
)

func TestWrapPreservesErrorAndAddsSafeCode(t *testing.T) {
	cause := errors.New("original failure")
	err := callbackdiagnostic.Wrap(cause, "presentation_missing")
	if err.Error() != cause.Error() || !errors.Is(err, cause) {
		t.Fatalf("error contract changed: %v", err)
	}
	if got := callbackdiagnostic.Inspect(fmt.Errorf("outer: %w", err)); got.Code != "presentation_missing" {
		t.Fatalf("details = %+v, want presentation_missing", got)
	}
	if callbackdiagnostic.Wrap(nil, "operation_failed") != nil {
		t.Fatal("Wrap(nil) must be nil")
	}
}

func TestAnnotatePreservesCodeIdentityAndMetadata(t *testing.T) {
	cause := errors.New("original failure")
	want := callbackdiagnostic.Details{Code: "presentation_replayed", Action: "page_next", SessionID: "session", TokenID: "token", PresentationID: "presentation", Target: 2, Retired: true, CardChatID: 42, CardMessageID: 99}
	details := want
	details.Code = "operation_failed"
	err := callbackdiagnostic.Annotate(callbackdiagnostic.Wrap(cause, want.Code), details)
	if got := callbackdiagnostic.Inspect(fmt.Errorf("outer: %w", err)); got != want {
		t.Fatalf("details = %+v, want %+v", got, want)
	}
	if err.Error() != cause.Error() || !errors.Is(err, cause) {
		t.Fatalf("error contract changed: %v", err)
	}
	if callbackdiagnostic.Annotate(nil, want) != nil {
		t.Fatal("Annotate(nil) must be nil")
	}
	plain := callbackdiagnostic.Annotate(cause, callbackdiagnostic.Details{Action: "page_next"})
	if got := callbackdiagnostic.Inspect(plain); got.Code != "" || got.Action != "page_next" || !errors.Is(plain, cause) || plain.Error() != cause.Error() {
		t.Fatalf("unclassified cause was lost: %+v, %v", got, plain)
	}
	if callbackdiagnostic.Inspect(nil) != (callbackdiagnostic.Details{}) || callbackdiagnostic.Inspect(cause) != (callbackdiagnostic.Details{}) {
		t.Fatal("plain errors must have no diagnostic details")
	}
}

func TestSafeCodeClosedAllowlist(t *testing.T) {
	for _, code := range []string{
		"token_invalid", "token_expired", "token_fields_invalid", "token_version_unsupported", "token_action_unknown",
		"presentation_missing", "presentation_replayed", "card_missing", "card_carrier_mismatch", "callback_origin_invalid",
		"callback_binding_mismatch", "registry_failed", "card_load_failed", "operation_failed", "cancelled", "deadline_exceeded",
		"card_commit_failed", "presentation_bind_failed", "provider_failure", "invalid_projection", "stale_presentation",
	} {
		if got := callbackdiagnostic.SafeCode(code); got != code {
			t.Errorf("SafeCode(%q) = %q", code, got)
		}
	}
	for _, code := range []string{"", "TOKEN_INVALID", "token_invalid\nsecret", "token_invalid ", "secret callback data", "unknown", "токен"} {
		if got := callbackdiagnostic.SafeCode(code); got != "operation_failed" {
			t.Errorf("unsafe code accepted: %q", got)
		}
		cause := errors.New("failure")
		for _, err := range []error{callbackdiagnostic.Wrap(cause, code), callbackdiagnostic.Annotate(cause, callbackdiagnostic.Details{Code: code})} {
			got := callbackdiagnostic.Inspect(err).Code
			if got != "operation_failed" && !(code == "" && got == "") {
				t.Errorf("unsafe wrapped code: %q", got)
			}
		}
	}
}
