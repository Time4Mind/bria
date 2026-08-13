package telegramapp_test

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramapp"
)

func TestMutedBackgroundNotificationIsNotSentAndIsDurablyConsumed(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := preferences.SetBackgroundNotification(domain.BackgroundFinished, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(300, 0).UTC()
	session := domain.Session{
		ID: "background-finished", NodeID: "allowed", OwnerID: 7, Name: "Finished",
		Backend: "codex", State: domain.SessionLive, RuntimePhase: domain.RuntimeRunning,
		CreatedAt: created, LiveSinceAt: created,
	}
	applyBackgroundCommand(t, fixture, "add-muted-background", clusterstate.CommandAddSession,
		created, session)
	stored := fixture.machine.State().Sessions[session.Ref().Key()]
	applyBackgroundCommand(t, fixture, "finish-muted-background",
		clusterstate.CommandPublishSessionRuntime, created.Add(time.Second),
		clusterstate.PublishSessionRuntime{
			Session: session.Ref(), Generation: stored.RuntimeGeneration,
			Phase: domain.RuntimeIdle,
		})
	handler, err := telegramapp.NewHandler(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 0 {
		t.Fatalf("muted notifications=%d", len(fixture.messenger.sent))
	}
	notice := fixture.machine.State().Navigation.BackgroundByUser[7][session.Ref().Key()]
	if notice.Kind != domain.BackgroundFinished || !notice.Notified {
		t.Fatalf("muted notice=%#v", notice)
	}
}

func applyBackgroundCommand(
	t *testing.T,
	fixture fixture,
	id string,
	kind clusterstate.CommandKind,
	at time.Time,
	payload any,
) {
	t.Helper()
	command, err := clusterstate.NewCommand(id, kind, at, payload)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
}
