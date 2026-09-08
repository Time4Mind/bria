// Package callbackdiagnostic carries structured callback failure context without
// changing the error presented to callers. It never parses callback data.
package callbackdiagnostic

import "errors"

type Details struct {
	Code, Action, SessionID, TokenID, PresentationID string
	Target                                           int
	Retired                                          bool
	CardChatID, CardMessageID                        int64
}

type failure struct {
	err     error
	details Details
}

func (f *failure) Error() string { return f.err.Error() }
func (f *failure) Unwrap() error { return f.err }

func Wrap(err error, code string) error {
	if err == nil {
		return nil
	}
	details := Inspect(err)
	details.Code = SafeCode(code)
	return &failure{err: err, details: details}
}
func Annotate(err error, details Details) error {
	if err == nil {
		return nil
	}
	if code := Inspect(err).Code; code != "" {
		details.Code = code
	} else if details.Code != "" {
		details.Code = SafeCode(details.Code)
	}
	return &failure{err: err, details: details}
}
func Inspect(err error) Details {
	var f *failure
	if errors.As(err, &f) {
		return f.details
	}
	return Details{}
}
func SafeCode(code string) string {
	switch code {
	case "token_invalid", "token_expired", "token_fields_invalid", "token_version_unsupported", "token_action_unknown",
		"presentation_missing", "presentation_replayed", "card_missing", "card_carrier_mismatch", "callback_origin_invalid",
		"callback_binding_mismatch", "registry_failed", "card_load_failed", "operation_failed", "cancelled", "deadline_exceeded",
		"card_commit_failed", "presentation_bind_failed":
		return code
	default:
		return "operation_failed"
	}
}
