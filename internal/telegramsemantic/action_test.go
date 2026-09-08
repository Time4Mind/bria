package telegramsemantic_test

import (
	"testing"

	"bria/internal/telegramsemantic"
)

// Characterization of the extracted public action contract; product validation
// is unchanged and remains exercised through the controller integration tests.
func TestActionValidationPreservesSessionAndMutationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name          string
		action        telegramsemantic.SemanticAction
		global, valid bool
	}{
		{"menu", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticMenuSessions}, true, true},
		{"menu-target", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticMenuSessions, SessionID: "s"}, true, false},
		{"back-to-card", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticMenuBack, SessionID: "s"}, true, true},
		{"create-without-claim", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticCreateConfirm}, true, false},
		{"claimed-create", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticCreateConfirm, UpdateID: 1}, true, true},
		{"page", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticPageNext, SessionID: "s", Page: 2}, false, true},
		{"page-without-session", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticPageNext, Page: 2}, false, false},
		{"latest", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticPageLatest, SessionID: "s", FollowLatest: true}, false, true},
		{"invalid-latest", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticPageLatest, SessionID: "s", Page: 1, FollowLatest: true}, false, false},
		{"native-key", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticNativeKey, SessionID: "s", Choice: 8}, false, true},
		{"native-key-overflow", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticNativeKey, SessionID: "s", Choice: 9}, false, false},
		{"close-overflow", telegramsemantic.SemanticAction{Kind: telegramsemantic.SemanticClose, SessionID: "s", Choice: 3}, false, false},
		{"unknown", telegramsemantic.SemanticAction{Kind: "unsupported", SessionID: "s"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := telegramsemantic.IsGlobal(tc.action.Kind); got != tc.global {
				t.Fatalf("global=%v want=%v", got, tc.global)
			}
			if err := telegramsemantic.ValidateAction(tc.action); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
