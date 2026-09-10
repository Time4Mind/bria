package telegramsemantic_test

import (
	"testing"

	"bria/internal/telegramsemantic"
)

func TestSatellitePreprocessingActionsAreGlobalAndTargetless(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind telegramsemantic.SemanticActionKind
		want string
	}{
		{"disabled", telegramsemantic.SemanticSettingsPreprocessingDisabled, "settings_preprocessing_disabled"},
		{"shared", telegramsemantic.SemanticSettingsPreprocessingShared, "settings_preprocessing_shared"},
		{"per-session", telegramsemantic.SemanticSettingsPreprocessingPerSession, "settings_preprocessing_per_session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(tc.kind); got != tc.want {
				t.Fatalf("action=%q want=%q", got, tc.want)
			}
			if !telegramsemantic.IsGlobal(tc.kind) {
				t.Fatal("satellite preprocessing action is not global")
			}
			if err := telegramsemantic.ValidateAction(telegramsemantic.SemanticAction{Kind: tc.kind}); err != nil {
				t.Fatal(err)
			}
			if err := telegramsemantic.ValidateAction(telegramsemantic.SemanticAction{Kind: tc.kind, SessionID: "session"}); err == nil {
				t.Fatal("global satellite preprocessing action accepted a session target")
			}
		})
	}
}
