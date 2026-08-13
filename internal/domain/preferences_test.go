package domain_test

import (
	"reflect"
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestTechnicalCardEventsAreVisibleByDefault(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	for _, eventType := range []domain.CardEventType{
		domain.CardEventToolCall,
		domain.CardEventToolResult,
		domain.CardEventThinking,
	} {
		if !preferences.ShowsCardEvent(eventType) {
			t.Fatalf("%q is hidden by default", eventType)
		}
	}
}

func TestVoiceBackendDefaultsAndValidation(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	if got := preferences.EffectiveVoiceBackend(); got != domain.VoiceOff {
		t.Fatalf("default voice backend=%q", got)
	}
	for _, backend := range []domain.VoiceBackend{
		domain.VoiceAuto, domain.VoiceWhisper, domain.VoiceApple, domain.VoiceOff,
	} {
		candidate := preferences
		candidate.VoiceBackend = backend
		if err := candidate.Validate(); err != nil {
			t.Fatalf("valid voice backend %q rejected: %v", backend, err)
		}
	}
	preferences.VoiceBackend = "remote"
	if err := preferences.Validate(); err == nil {
		t.Fatal("unknown voice backend accepted")
	}
}

func TestToolOutputLinesDefaultAndValidation(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	if got := preferences.EffectiveToolOutputLines(); got != 15 {
		t.Fatalf("default tool output lines=%d", got)
	}
	legacy := preferences
	legacy.ToolOutputLines = 0
	if err := legacy.Validate(); err != nil || legacy.EffectiveToolOutputLines() != 15 {
		t.Fatalf("legacy preference does not use 15 lines: %#v, %v", legacy, err)
	}
	for _, lines := range []int{5, 10, 15, 20, 25, 30} {
		candidate := preferences
		candidate.ToolOutputLines = lines
		if err := candidate.Validate(); err != nil {
			t.Fatalf("valid line limit %d rejected: %v", lines, err)
		}
	}
	for _, lines := range []int{-5, 1, 14, 31} {
		candidate := preferences
		candidate.ToolOutputLines = lines
		if err := candidate.Validate(); err == nil {
			t.Fatalf("invalid line limit %d accepted", lines)
		}
	}
}

func TestResponseCardModesAreClosedAndLegacyDefaultsToPagination(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	if got := preferences.EffectiveResponseCards(); got != domain.ResponseCardsKeepPaginated {
		t.Fatalf("default response cards=%q", got)
	}
	for _, mode := range []domain.ResponseCardMode{
		domain.ResponseCardsKeepPaginated,
		domain.ResponseCardsKeepLatest,
		domain.ResponseCardsReplace,
	} {
		candidate := preferences
		candidate.ResponseCards = mode
		if err := candidate.Validate(); err != nil {
			t.Fatalf("valid mode %q rejected: %v", mode, err)
		}
	}
	legacy := preferences
	legacy.ResponseCards = ""
	if legacy.Validate() != nil || legacy.EffectiveResponseCards() != domain.ResponseCardsKeepPaginated {
		t.Fatalf("legacy mode did not preserve pagination: %#v", legacy)
	}
	preferences.ResponseCards = "unknown"
	if err := preferences.Validate(); err == nil {
		t.Fatal("unknown response card mode accepted")
	}
}

func TestTechnicalCardVisibilityIsIndependentAndCanonical(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	for _, eventType := range []domain.CardEventType{
		domain.CardEventThinking,
		domain.CardEventToolCall,
		domain.CardEventToolResult,
	} {
		if err := preferences.SetCardEventVisibility(eventType, false); err != nil {
			t.Fatal(err)
		}
	}
	want := []domain.CardEventType{
		domain.CardEventToolCall,
		domain.CardEventToolResult,
		domain.CardEventThinking,
	}
	if !reflect.DeepEqual(preferences.HiddenCardEvents, want) {
		t.Fatalf("hidden card events=%#v, want %#v", preferences.HiddenCardEvents, want)
	}
	if err := preferences.SetCardEventVisibility(domain.CardEventToolResult, true); err != nil {
		t.Fatal(err)
	}
	if !preferences.ShowsCardEvent(domain.CardEventToolResult) ||
		preferences.ShowsCardEvent(domain.CardEventToolCall) ||
		preferences.ShowsCardEvent(domain.CardEventThinking) {
		t.Fatalf("visibility is not independent: %#v", preferences)
	}
}

func TestTechnicalCardVisibilityRejectsUnknownAndDuplicateValues(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	if err := preferences.SetCardEventVisibility("unknown", false); err == nil {
		t.Fatal("unknown card event was accepted")
	}
	preferences.HiddenCardEvents = []domain.CardEventType{
		domain.CardEventToolCall,
		domain.CardEventToolCall,
	}
	if err := preferences.Validate(); err == nil {
		t.Fatal("duplicate hidden card event was accepted")
	}
}

func TestBackgroundNotificationPreferencesAreIndependentAndClosed(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	if err := preferences.SetBackgroundNotification(domain.BackgroundError, false); err != nil {
		t.Fatal(err)
	}
	if preferences.SendsBackgroundNotification(domain.BackgroundError) ||
		!preferences.SendsBackgroundNotification(domain.BackgroundFinished) ||
		!preferences.SendsBackgroundNotification(domain.BackgroundNeedsAction) {
		t.Fatalf("notification preferences are not independent: %#v", preferences)
	}
	preferences.BackgroundDismissSwitches = 10
	if err := preferences.Validate(); err != nil {
		t.Fatalf("valid background preferences rejected: %v", err)
	}
	preferences.BackgroundDismissSwitches = 2
	if err := preferences.Validate(); err == nil {
		t.Fatal("unsupported dismissal count accepted")
	}
	preferences = domain.DefaultUserPreferences()
	preferences.MutedBackgroundNotifications = []domain.BackgroundNoticeKind{
		domain.BackgroundError, domain.BackgroundError,
	}
	if err := preferences.Validate(); err == nil {
		t.Fatal("duplicate muted notification accepted")
	}
}

func TestStateCanonicalizesAndDeepCopiesHiddenCardEvents(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	preferences := domain.DefaultUserPreferences()
	preferences.HiddenCardEvents = []domain.CardEventType{
		domain.CardEventThinking,
		domain.CardEventToolCall,
	}
	if err := state.SetPreferences(1, preferences); err != nil {
		t.Fatal(err)
	}
	want := []domain.CardEventType{domain.CardEventToolCall, domain.CardEventThinking}
	if !reflect.DeepEqual(state.Preferences[1].HiddenCardEvents, want) {
		t.Fatalf("stored hidden events=%#v", state.Preferences[1].HiddenCardEvents)
	}
	clone := state.Clone()
	clonePreferences := clone.Preferences[1]
	clonePreferences.HiddenCardEvents[0] = domain.CardEventToolResult
	clone.Preferences[1] = clonePreferences
	if !reflect.DeepEqual(state.Preferences[1].HiddenCardEvents, want) {
		t.Fatalf("clone aliased source preferences: %#v", state.Preferences[1].HiddenCardEvents)
	}
}

func TestResponseCardRegistryIsBoundToThePrivateActorChat(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordTelegramResponseCard(7, domain.TelegramResponseCard{
		ChatID: 8, MessageID: 1,
	}); err == nil {
		t.Fatal("response card from another chat was accepted")
	}
}
