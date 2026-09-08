package telegramflow_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegram"
)

func TestAuthenticatedIngressResetsActualSchedulerWithoutReplayReset(t *testing.T) {
	ctx := context.Background()
	start := time.Unix(1_800_000_000, 0)
	now := start
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{
		Now: func() time.Time { return now },
		Wait: func(ctx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			now = now.Add(d)
			return nil
		},
		Jitter: func(time.Duration) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	acquire := func(want time.Duration) {
		t.Helper()
		before := now
		lease, err := scheduler.Acquire(ctx, telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 100})
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.Complete(telegram.MutationOutcome{HTTPStatus: 200}); err != nil {
			t.Fatal(err)
		}
		if got := now.Sub(before); got != want {
			t.Fatalf("actual scheduling delay=%v want=%v", got, want)
		}
	}
	handler := newActivityHandler(t, func(u coordinator.Update) { scheduler.RecordUserActivity(u.ConversationID, u.ID) }, nil)
	update := activityUpdate()
	if _, err := handler.Handle(ctx, update); err != nil {
		t.Fatal(err)
	}
	acquire(0)
	now = start.Add(time.Minute)
	acquire(0)
	acquire(2500 * time.Millisecond)
	// An actual message resets cadence while preserving the last send spacing.
	update.ID = 11
	update.MediaKind = "voice"
	update.Text = ""
	if _, err := handler.Handle(ctx, update); err != nil {
		t.Fatal(err)
	}
	resetAt := now
	acquire(1500 * time.Millisecond)
	now = resetAt.Add(30 * time.Second)
	acquire(0)
	for _, invalid := range []coordinator.Update{
		update,
		{ID: 10, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private"},
		{ID: 12, Kind: coordinator.UpdateMessage, ActorID: 8, ConversationID: 42, ConversationKind: "private"},
		{ID: 13, Kind: coordinator.UpdateUnsupported, ActorID: 7, ConversationID: 42, ConversationKind: "private"},
	} {
		_, _ = handler.Handle(ctx, invalid)
		acquire(2 * time.Second)
	}
	// Stale/invalid owner buttons are still actual user actions before decoding.
	update.ID = 14
	update.Kind = coordinator.UpdateCallback
	update.Text = "stale-native-key"
	update.CallbackQueryID = "synthetic-query"
	_, _ = handler.Handle(ctx, update)
	acquire(1500 * time.Millisecond)
	now = now.Add(time.Minute)
	acquire(0)
	// Background recovery projections and later sends cannot rejuvenate cadence.
	_, _, _ = handler.PrepareUnknownRecovery(ctx, update)
	_, _ = handler.ListUnknown(ctx, 10)
	acquire(2500 * time.Millisecond)
}
