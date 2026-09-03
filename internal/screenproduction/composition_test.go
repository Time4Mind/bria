package screenproduction_test

import (
	"context"
	"testing"

	"bria/internal/domain"
	"bria/internal/screen"
	"bria/internal/screenproduction"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/telegram"
	"bria/internal/turnprocessing"
)

type photoSenderFunc func(context.Context, telegram.SendPhotoRequest) (telegram.PhotoReceipt, error)

func (function photoSenderFunc) SendPhoto(ctx context.Context, request telegram.SendPhotoRequest) (telegram.PhotoReceipt, error) {
	return function(ctx, request)
}

func TestTypedEventsReachVirtualScreenButPhotoRequiresGlobalSettingAndExactReceipt(t *testing.T) {
	preferences := settings.NewMemoryStore()
	store, err := screen.New(screen.Options{MaxSessions: 4, MaxLines: 20, MaxColumns: 80, MaxEventBytes: 1024, MaxPNGBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var requests []telegram.SendPhotoRequest
	composition, err := screenproduction.Open(screenproduction.Config{
		Store: store, Settings: preferences, ChatID: 42,
		Sender: photoSenderFunc(func(_ context.Context, request telegram.SendPhotoRequest) (telegram.PhotoReceipt, error) {
			requests = append(requests, request)
			return telegram.PhotoReceipt{ChatID: request.ChatID, MessageID: 900 + telegram.MessageID(len(requests))}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := domain.SessionID("11111111-1111-4111-9111-111111111111")
	disabled := turnprocessing.RuntimeEventObservation{OperationID: "m-1:event:1", SessionID: sessionID, MessageID: "m-1", EventIndex: 1, Event: sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: "safe output"}}
	if err := composition.ObserveRuntimeEvent(context.Background(), disabled); err != nil {
		t.Fatalf("ObserveRuntimeEvent(disabled) error = %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("disabled global Screen sent %d photos", len(requests))
	}
	snapshot, err := store.Snapshot(context.Background(), sessionID)
	if err != nil || len(snapshot.Lines) != 1 || snapshot.Lines[0] != "safe output" {
		t.Fatalf("disabled screen snapshot = %#v, %v", snapshot, err)
	}
	if err := preferences.Update(context.Background(), func(value *settings.Settings) error { value.ScreenEnabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	enabled := disabled
	enabled.OperationID, enabled.EventIndex, enabled.Event.Text = "m-1:event:2", 2, "next safe output"
	if err := composition.ObserveRuntimeEvent(context.Background(), enabled); err != nil {
		t.Fatalf("ObserveRuntimeEvent(enabled) error = %v", err)
	}
	if len(requests) != 1 || requests[0].ChatID != 42 || requests[0].ContentType != "image/png" || len(requests[0].Content) == 0 {
		t.Fatalf("photo requests = %#v", requests)
	}
	if err := composition.ObserveRuntimeEvent(context.Background(), enabled); err != nil {
		t.Fatalf("ObserveRuntimeEvent(replay) error = %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("replayed event sent %d photos", len(requests))
	}
}
