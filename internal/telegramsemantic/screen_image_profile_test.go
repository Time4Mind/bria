package telegramsemantic_test

import (
	"testing"

	"bria/internal/telegramsemantic"
)

func TestScreenImageProfileActionIsGlobalAndTargetless(t *testing.T) {
	kind := telegramsemantic.SemanticSettingsScreenImageProfile
	if !telegramsemantic.IsGlobal(kind) {
		t.Fatal("screen image profile setting is not global")
	}
	if err := telegramsemantic.ValidateAction(telegramsemantic.SemanticAction{Kind: kind}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []telegramsemantic.SemanticAction{
		{Kind: kind, SessionID: "session"},
		{Kind: kind, Page: 1},
		{Kind: kind, Choice: 1},
		{Kind: kind, FollowLatest: true},
		{Kind: kind, SessionSlot: 1},
	} {
		if err := telegramsemantic.ValidateAction(action); err == nil {
			t.Fatalf("invalid target accepted: %+v", action)
		}
	}
}
