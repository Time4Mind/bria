package telegramui_test

import (
	"reflect"
	"testing"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/telegramui"
)

func TestCreationPreviewExposesOnlyConfirmedProductChoices(t *testing.T) {
	intent := app.ConfirmedSessionIntent{
		IntentID:   "intent-private",
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	}

	got := telegramui.PreviewCreation(intent)
	want := telegramui.CreationPreview{
		Computer: "computer-1",
		Provider: domain.ProviderCodex,
		Workdir:  "/workspace/project",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PreviewCreation() = %#v, want %#v", got, want)
	}
	assertExportedFields(t, got, "Computer", "Provider", "Workdir")
}

func TestSessionCardProjectsSupportedLifecycleStatesWithoutTechnicalIdentity(t *testing.T) {
	starting := mustStartingSession(t)
	ready, err := starting.Ready(domain.ProviderBinding{
		Provider:   domain.ProviderClaude,
		SessionID:  "provider-session-private",
		Generation: 7,
	})
	if err != nil {
		t.Fatalf("mark session ready: %v", err)
	}
	awaitingRecovery, err := starting.AwaitRecovery()
	if err != nil {
		t.Fatalf("mark session awaiting recovery: %v", err)
	}

	tests := []struct {
		name    string
		session domain.Session
		state   telegramui.SessionState
	}{
		{name: "starting", session: starting, state: telegramui.SessionStarting},
		{name: "ready", session: ready, state: telegramui.SessionReady},
		{name: "awaiting recovery", session: awaitingRecovery, state: telegramui.SessionAwaitingRecovery},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := telegramui.ProjectSessionCard(test.session)
			want := telegramui.SessionCard{
				Computer: "computer-1",
				Provider: domain.ProviderClaude,
				Workdir:  "/workspace/project",
				State:    test.state,
			}

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ProjectSessionCard() = %#v, want %#v", got, want)
			}
			assertExportedFields(t, got, "Computer", "Provider", "Workdir", "State")
		})
	}
}

func mustStartingSession(t *testing.T) domain.Session {
	t.Helper()
	session, err := domain.NewStartingSession(
		"bria-session-private",
		"intent-private",
		"computer-1",
		domain.ProviderClaude,
		"/workspace/project",
	)
	if err != nil {
		t.Fatalf("create starting session: %v", err)
	}
	return session
}

func assertExportedFields(t *testing.T, value any, want ...string) {
	t.Helper()
	typeOf := reflect.TypeOf(value)
	if got := typeOf.NumField(); got != len(want) {
		t.Fatalf("%s exports %d fields, want %d", typeOf, got, len(want))
	}
	for index, fieldName := range want {
		field := typeOf.Field(index)
		if field.Name != fieldName || !field.IsExported() {
			t.Fatalf("%s field %d = %q (exported %v), want exported %q", typeOf, index, field.Name, field.IsExported(), fieldName)
		}
	}
}
