package coordinator_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"bria/internal/coordinator"
)

func TestFirstRunVerifiesIdentityThenQuarantinesBacklogBeforePolling(t *testing.T) {
	store := newMemoryStore()
	source := &fakeSource{bootstrapNext: 41, pollErr: context.Canceled}
	handler := &fakeHandler{}
	ready := &fakeReadiness{check: func(checkpoint coordinator.Checkpoint) {
		if checkpoint != (coordinator.Checkpoint{}) {
			t.Fatalf("identity gate checkpoint = %#v, want zero before first bootstrap", checkpoint)
		}
	}}
	source.beforePoll = func(next int64) {
		checkpoint := store.checkpoint()
		if !checkpoint.Initialized || checkpoint.NextUpdateID != next {
			t.Fatalf("checkpoint before poll = %#v, want persisted offset %d", checkpoint, next)
		}
		if got := store.loadCount(); got < 2 {
			t.Fatalf("store loads before poll = %d, want initial load plus persisted reread", got)
		}
	}

	loop := newLoop(t, source, store, handler, &fakeSender{}, ready)
	err := loop.Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	if got, want := source.bootstrapCalls, 1; got != want {
		t.Errorf("bootstrap calls = %d, want %d", got, want)
	}
	if got := handler.callCount(); got != 0 {
		t.Errorf("handler calls for quarantined backlog = %d, want 0", got)
	}
	if got, want := source.pollOffsets, []int64{41}; !reflect.DeepEqual(got, want) {
		t.Errorf("poll offsets = %v, want %v", got, want)
	}
	if got := ready.callCount(); got != 1 {
		t.Errorf("ready calls = %d, want 1", got)
	}
}

func TestIdentityFailurePreventsDestructiveBootstrapAndStateMutation(t *testing.T) {
	store := newMemoryStore()
	source := &fakeSource{bootstrapNext: 41}
	ready := &fakeReadiness{err: errors.New("authenticated bot identity does not match")}

	err := newLoop(t, source, store, &fakeHandler{}, &fakeSender{}, ready).Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want identity rejection")
	}
	if source.bootstrapCalls != 0 {
		t.Fatalf("bootstrap calls after identity rejection = %d, want 0", source.bootstrapCalls)
	}
	if store.saveCount() != 0 {
		t.Fatalf("checkpoint saves after identity rejection = %d, want 0", store.saveCount())
	}
	if ready.callCount() != 1 {
		t.Fatalf("ready calls = %d, want exactly 1", ready.callCount())
	}
}

func TestFirstRunAcceptsZeroOffsetWhenBootstrapBacklogIsEmpty(t *testing.T) {
	store := newMemoryStore()
	source := &fakeSource{bootstrapNext: 0, pollErr: context.Canceled}
	ready := &fakeReadiness{}

	err := newLoop(t, source, store, &fakeHandler{}, &fakeSender{}, ready).Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	if got := store.checkpoint(); !got.Initialized || got.NextUpdateID != 0 {
		t.Fatalf("checkpoint = %#v, want initialized zero offset", got)
	}
	if got, want := source.pollOffsets, []int64{0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("poll offsets = %v, want %v", got, want)
	}
	if ready.callCount() != 1 {
		t.Fatal("zero-offset checkpoint was not ready")
	}
}

func TestLiveUpdatesAdvanceOnlyAfterDurableSkipOrPositiveSendReceipt(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{Initialized: true, NextUpdateID: 10})
	source := &fakeSource{
		batches: [][]coordinator.Update{{
			{ID: 10, Kind: coordinator.UpdateMessage, ActorID: 1, ConversationID: 7, ConversationKind: "private", Text: "unauthorized"},
			{ID: 12, Kind: coordinator.UpdateMessage, ActorID: 2, ConversationID: 7, ConversationKind: "private", Text: "/status"},
		}},
		pollErr: context.Canceled,
	}
	handler := &fakeHandler{decisions: map[int64]coordinator.Decision{
		10: {Kind: coordinator.DecisionSkip},
		12: {
			Kind:   coordinator.DecisionStatus,
			Status: coordinator.Status{ConversationID: 7, Text: "safe status"},
		},
	}}
	sender := &fakeSender{receipt: coordinator.Receipt{MessageID: 91}}
	sender.beforeSend = func(operationID string, status coordinator.Status) {
		stored := store.checkpoint()
		if stored.NextUpdateID != 11 {
			t.Fatalf("offset at send = %d, want 11", stored.NextUpdateID)
		}
		if stored.Outbound == nil || stored.Outbound.Phase != coordinator.OutboundPrepared {
			t.Fatalf("outbound at send = %#v, want durable prepared", stored.Outbound)
		}
		if stored.Outbound.OperationID != operationID || stored.Outbound.Status != status {
			t.Fatalf("durable outbound = %#v, send = (%q, %#v)", stored.Outbound, operationID, status)
		}
	}

	loop := newLoop(t, source, store, handler, sender, &fakeReadiness{})
	err := loop.Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	checkpoint := store.checkpoint()
	if got, want := checkpoint.NextUpdateID, int64(13); got != want {
		t.Errorf("next update id = %d, want %d", got, want)
	}
	if checkpoint.Outbound == nil || checkpoint.Outbound.Phase != coordinator.OutboundConfirmed ||
		checkpoint.Outbound.Receipt == nil || checkpoint.Outbound.Receipt.MessageID != 91 {
		t.Errorf("confirmed outbound = %#v, want positive receipt", checkpoint.Outbound)
	}
	if got, want := handler.ids(), []int64{10, 12}; !reflect.DeepEqual(got, want) {
		t.Errorf("handled ids = %v, want %v", got, want)
	}
}

func TestMessageMetadataReachesHandlerWithoutLosingReplyOrMediaIdentity(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{Initialized: true, NextUpdateID: 100})
	want := coordinator.Update{
		ID: 100, Kind: coordinator.UpdateMessage, ActorID: 2,
		ConversationID: 7, ConversationKind: "private",
		Text: "caption text", Caption: "caption text",
		SourceMessageID: 501, ReplyToMessageID: 499,
		MediaKind: "photo", MediaFileID: "file-id", MediaFileUniqueID: "unique-id",
		MediaFileSize: 1234, MediaMIMEType: "image/jpeg", MediaWidth: 640, MediaHeight: 480,
		MediaDownloadAllowed: true,
	}
	source := &fakeSource{batches: [][]coordinator.Update{{want}}, pollErr: context.Canceled}
	handler := &fakeHandler{decisions: map[int64]coordinator.Decision{100: {Kind: coordinator.DecisionSkip}}}

	err := newLoop(t, source, store, handler, &fakeSender{}, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	if got := handler.updates(); !reflect.DeepEqual(got, []coordinator.Update{want}) {
		t.Fatalf("handler updates = %#v, want %#v", got, []coordinator.Update{want})
	}
}

func TestAmbiguousSendIsDurableAndNeverAutomaticallyRetried(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{Initialized: true, NextUpdateID: 20})
	source := &fakeSource{batches: [][]coordinator.Update{{{
		ID: 20, Kind: coordinator.UpdateMessage, ActorID: 2,
		ConversationID: 7, ConversationKind: "private", Text: "/status",
	}}}}
	handler := &fakeHandler{decisions: map[int64]coordinator.Decision{20: {
		Kind:   coordinator.DecisionStatus,
		Status: coordinator.Status{ConversationID: 7, Text: "safe"},
	}}}
	sender := &fakeSender{err: errors.New("network result is unknown")}

	err := newLoop(t, source, store, handler, sender, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, coordinator.ErrDeliveryUnknown) {
		t.Fatalf("first Run() error = %v, want ErrDeliveryUnknown", err)
	}
	checkpoint := store.checkpoint()
	if checkpoint.NextUpdateID != 20 || checkpoint.Outbound == nil || checkpoint.Outbound.Phase != coordinator.OutboundUnknown {
		t.Fatalf("checkpoint after ambiguous send = %#v", checkpoint)
	}

	restartSource := &fakeSource{pollErr: errors.New("must not poll")}
	restartSender := &fakeSender{}
	err = newLoop(t, restartSource, store, &fakeHandler{}, restartSender, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, coordinator.ErrDeliveryUnknown) {
		t.Fatalf("restart Run() error = %v, want ErrDeliveryUnknown", err)
	}
	if restartSource.pollCalls != 0 || restartSender.callCount() != 0 {
		t.Fatalf("restart poll/send calls = %d/%d, want 0/0", restartSource.pollCalls, restartSender.callCount())
	}
}

func TestUnknownHandlerEffectAdvancesOnlyAfterDurableRecoveryControl(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{Initialized: true, NextUpdateID: 20})
	update := coordinator.Update{
		ID: 20, Kind: coordinator.UpdateCallback, ActorID: 2, ConversationID: 7,
		ConversationKind: "private", Text: "signed-callback", CallbackQueryID: "query-20", SourceMessageID: 99,
	}
	source := &fakeSource{batches: [][]coordinator.Update{{update}}, pollErr: context.Canceled}
	handler := &recoveryHandler{unknown: update.ID}
	sender := &fakeDurableSender{}

	err := newLoop(t, source, store, handler, sender, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled after durable recovery control", err)
	}
	if handler.recoveryCalls != 1 || sender.enqueueCalls != 1 {
		t.Fatalf("recovery/enqueue calls = %d/%d, want 1/1", handler.recoveryCalls, sender.enqueueCalls)
	}
	checkpoint := store.checkpoint()
	if checkpoint.NextUpdateID != 21 || checkpoint.Recovery == nil ||
		checkpoint.Recovery.OriginalOperationID != "status:20" || checkpoint.Recovery.PromptOperationID != "recovery:20" ||
		checkpoint.Recovery.UpdateID != 20 {
		t.Fatalf("checkpoint recovery = %#v, want exact durable recovery control and advanced offset", checkpoint)
	}
	if checkpoint.Outbound == nil || checkpoint.Outbound.OperationID != "recovery:20" || checkpoint.Outbound.Phase != coordinator.OutboundEnqueued {
		t.Fatalf("recovery prompt outbound = %#v", checkpoint.Outbound)
	}
}

func TestConfirmedSendIsNotDuplicatedAfterRestartAndLaterSkipsCanAdvance(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{
		Initialized:  true,
		NextUpdateID: 81,
		Outbound: &coordinator.Outbound{
			OperationID: "status:80",
			UpdateID:    80,
			Status:      coordinator.Status{ConversationID: 7, Text: "safe"},
			Phase:       coordinator.OutboundConfirmed,
			Receipt:     &coordinator.Receipt{MessageID: 900},
		},
	})
	source := &fakeSource{
		batches: [][]coordinator.Update{{{
			ID: 83, Kind: coordinator.UpdateMessage, ActorID: 1,
			ConversationID: 7, ConversationKind: "private", Text: "unauthorized",
		}}},
		pollErr: context.Canceled,
	}
	handler := &fakeHandler{decisions: map[int64]coordinator.Decision{83: {Kind: coordinator.DecisionSkip}}}
	sender := &fakeSender{}

	err := newLoop(t, source, store, handler, sender, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	if sender.callCount() != 0 {
		t.Fatalf("restart send calls = %d, want 0", sender.callCount())
	}
	checkpoint := store.checkpoint()
	if checkpoint.NextUpdateID != 84 || checkpoint.Outbound == nil ||
		checkpoint.Outbound.Receipt == nil || checkpoint.Outbound.Receipt.MessageID != 900 {
		t.Fatalf("checkpoint after later skip = %#v, want offset 84 with prior receipt retained", checkpoint)
	}
}

func TestPreparedOutboundAfterCrashBecomesUnknownWithoutSend(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{
		Initialized:  true,
		NextUpdateID: 30,
		Outbound: &coordinator.Outbound{
			OperationID: "status:30",
			UpdateID:    30,
			Status:      coordinator.Status{ConversationID: 7, Text: "safe"},
			Phase:       coordinator.OutboundPrepared,
		},
	})
	sender := &fakeSender{}
	err := newLoop(t, &fakeSource{}, store, &fakeHandler{}, sender, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, coordinator.ErrDeliveryUnknown) {
		t.Fatalf("Run() error = %v, want ErrDeliveryUnknown", err)
	}
	if got := store.checkpoint().Outbound; got == nil || got.Phase != coordinator.OutboundUnknown {
		t.Fatalf("outbound = %#v, want unknown", got)
	}
	if sender.callCount() != 0 {
		t.Fatal("prepared outbound was resent after restart")
	}
}

func TestDurableSenderAcceptanceAdvancesCheckpointBeforeTelegramDelivery(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{Initialized: true, NextUpdateID: 300})
	source := &fakeSource{batches: [][]coordinator.Update{{{
		ID: 300, Kind: coordinator.UpdateMessage, ActorID: 2,
		ConversationID: 7, ConversationKind: "private", Text: "/status",
	}}, {{
		ID: 301, Kind: coordinator.UpdateMessage, ActorID: 1,
		ConversationID: 7, ConversationKind: "private", Text: "later",
	}}}, pollErr: context.Canceled}
	handler := &fakeHandler{decisions: map[int64]coordinator.Decision{
		300: {Kind: coordinator.DecisionStatus, Status: coordinator.Status{ConversationID: 7, Text: "safe"}},
		301: {Kind: coordinator.DecisionSkip},
	}}
	sender := &fakeDurableSender{fakeSender: fakeSender{err: errors.New("external SendStatus must not be used")}}
	err := newLoop(t, source, store, handler, sender, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if sender.enqueueCalls != 1 || sender.callCount() != 0 {
		t.Fatalf("durable/external calls = %d/%d", sender.enqueueCalls, sender.callCount())
	}
	checkpoint := store.checkpoint()
	if checkpoint.NextUpdateID != 302 || checkpoint.Outbound == nil || checkpoint.Outbound.Phase != coordinator.OutboundEnqueued ||
		checkpoint.Outbound.Durable == nil || checkpoint.Outbound.Durable.OperationID != "status:300" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	// The inner sender may still be unknown at Telegram, but restart must keep
	// polling because its signed recovery is independent of inbound custody.
	sender.resolved = true
	sender.resolvedReceipt = coordinator.Receipt{MessageID: 902}
	restart := newLoop(t, &fakeSource{pollErr: context.Canceled}, store, &fakeHandler{}, sender, &fakeReadiness{})
	if err := restart.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("restart Run() error = %v", err)
	}
	checkpoint = store.checkpoint()
	if checkpoint.NextUpdateID != 302 || checkpoint.Outbound.Phase != coordinator.OutboundConfirmed || checkpoint.Outbound.Receipt.MessageID != 902 {
		t.Fatalf("confirmed inner receipt checkpoint = %#v", checkpoint)
	}
}

func TestConfirmedDurableOutboundAndNextUpdateAreSerializedWithoutLostState(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{
		Initialized:  true,
		NextUpdateID: 301,
		Outbound: &coordinator.Outbound{
			OperationID: "status:300",
			UpdateID:    300,
			Status:      coordinator.Status{ConversationID: 7, Text: "safe"},
			Phase:       coordinator.OutboundEnqueued,
			Durable:     &coordinator.DurableOutboundReceipt{OperationID: "status:300", Sequence: 1},
		},
	})
	pollStarted := make(chan struct{})
	releasePoll := make(chan struct{})
	var blockFirstPoll sync.Once
	source := &fakeSource{
		batches: [][]coordinator.Update{{{
			ID: 301, Kind: coordinator.UpdateMessage, ActorID: 1,
			ConversationID: 7, ConversationKind: "private", Text: "later",
		}}},
		pollErr: context.Canceled,
	}
	source.beforePoll = func(int64) {
		blockFirstPoll.Do(func() {
			close(pollStarted)
			<-releasePoll
		})
	}
	sender := &fakeDurableSender{}
	handler := &fakeHandler{decisions: map[int64]coordinator.Decision{
		301: {Kind: coordinator.DecisionSkip},
	}}
	loop := newLoop(t, source, store, handler, sender, &fakeReadiness{})
	runDone := make(chan error, 1)
	go func() { runDone <- loop.Run(context.Background()) }()

	<-pollStarted
	sender.resolved = true
	sender.resolvedReceipt = coordinator.Receipt{MessageID: 902}
	confirmed, err := loop.ConfirmEnqueuedOutbound(context.Background(), "status:300", 300)
	if err != nil {
		t.Fatalf("ConfirmEnqueuedOutbound() error = %v", err)
	}
	if confirmed.Checkpoint.NextUpdateID != 301 || confirmed.Checkpoint.Outbound == nil ||
		confirmed.Checkpoint.Outbound.Phase != coordinator.OutboundConfirmed {
		t.Fatalf("confirmed checkpoint = %#v", confirmed.Checkpoint)
	}
	close(releasePoll)
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}

	checkpoint := store.checkpoint()
	if checkpoint.NextUpdateID != 302 || checkpoint.Outbound == nil ||
		checkpoint.Outbound.Phase != coordinator.OutboundConfirmed || checkpoint.Outbound.Receipt == nil ||
		checkpoint.Outbound.Receipt.MessageID != 902 {
		t.Fatalf("checkpoint after concurrent confirmation/update = %#v", checkpoint)
	}
}

func TestStartupRecoveryReloadsCheckpointAfterConcurrentConfirmation(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{
		Initialized:  true,
		NextUpdateID: 301,
		Outbound: &coordinator.Outbound{
			OperationID: "status:300",
			UpdateID:    300,
			Status:      coordinator.Status{ConversationID: 7, Text: "safe"},
			Phase:       coordinator.OutboundEnqueued,
			Durable:     &coordinator.DurableOutboundReceipt{OperationID: "status:300", Sequence: 1},
		},
	})
	readinessStarted := make(chan struct{})
	releaseReadiness := make(chan struct{})
	ready := &fakeReadiness{check: func(coordinator.Checkpoint) {
		close(readinessStarted)
		<-releaseReadiness
	}}
	sender := &fakeDurableSender{resolved: true, resolvedReceipt: coordinator.Receipt{MessageID: 902}}
	loop := newLoop(t, &fakeSource{pollErr: context.Canceled}, store, &fakeHandler{}, sender, ready)
	runDone := make(chan error, 1)
	go func() { runDone <- loop.Run(context.Background()) }()

	<-readinessStarted
	if _, err := loop.ConfirmEnqueuedOutbound(context.Background(), "status:300", 300); err != nil {
		t.Fatalf("ConfirmEnqueuedOutbound() error = %v", err)
	}
	close(releaseReadiness)
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}

	checkpoint := store.checkpoint()
	if checkpoint.NextUpdateID != 301 || checkpoint.Outbound == nil ||
		checkpoint.Outbound.Phase != coordinator.OutboundConfirmed || checkpoint.Outbound.Receipt == nil ||
		checkpoint.Outbound.Receipt.MessageID != 902 {
		t.Fatalf("checkpoint after startup confirmation = %#v", checkpoint)
	}
}

func TestAuthorizedUnknownCommandBlocksAtExactUpdate(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{Initialized: true, NextUpdateID: 50})
	source := &fakeSource{batches: [][]coordinator.Update{{{
		ID: 52, Kind: coordinator.UpdateMessage, ActorID: 2,
		ConversationID: 7, ConversationKind: "private", Text: "/future",
	}}}}
	handler := &fakeHandler{decisions: map[int64]coordinator.Decision{52: {
		Kind:        coordinator.DecisionBlock,
		BlockReason: "authorized command is not implemented",
	}}}

	err := newLoop(t, source, store, handler, &fakeSender{}, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, coordinator.ErrUpdateBlocked) {
		t.Fatalf("Run() error = %v, want ErrUpdateBlocked", err)
	}
	checkpoint := store.checkpoint()
	if checkpoint.NextUpdateID != 50 || checkpoint.Blocked == nil || checkpoint.Blocked.UpdateID != 52 {
		t.Fatalf("blocked checkpoint = %#v, want exact update 52 without advance", checkpoint)
	}

	restartHandler := &fakeHandler{}
	err = newLoop(t, &fakeSource{}, store, restartHandler, &fakeSender{}, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, coordinator.ErrUpdateBlocked) || restartHandler.callCount() != 0 {
		t.Fatalf("restart error/handler calls = %v/%d, want durable block/0", err, restartHandler.callCount())
	}
}

func TestCallbackIdentityAndCarrierReachHandlerWithoutBecomingMessage(t *testing.T) {
	store := newMemoryStoreWith(coordinator.Checkpoint{Initialized: true, NextUpdateID: 90})
	callback := coordinator.Update{
		ID:               90,
		Kind:             coordinator.UpdateCallback,
		ActorID:          2,
		ConversationID:   7,
		ConversationKind: "private",
		Text:             "/status",
		CallbackQueryID:  "opaque-query-id",
		SourceMessageID:  901,
	}
	source := &fakeSource{batches: [][]coordinator.Update{{callback}}, pollErr: context.Canceled}
	handler := &fakeHandler{decisions: map[int64]coordinator.Decision{90: {Kind: coordinator.DecisionSkip}}}

	err := newLoop(t, source, store, handler, &fakeSender{}, &fakeReadiness{}).Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	if got := handler.updates(); !reflect.DeepEqual(got, []coordinator.Update{callback}) {
		t.Fatalf("handler updates = %#v, want exact callback %#v", got, callback)
	}
}

func TestRejectsInconsistentUpdateKindPayloadBeforeHandler(t *testing.T) {
	tests := []struct {
		name   string
		update coordinator.Update
	}{
		{name: "unsupported with actor", update: coordinator.Update{ID: 100, ActorID: 1}},
		{name: "unsupported with text", update: coordinator.Update{ID: 100, Text: "/status"}},
		{name: "message without actor", update: coordinator.Update{ID: 100, Kind: coordinator.UpdateMessage, ConversationID: 7, ConversationKind: "private"}},
		{name: "message without conversation", update: coordinator.Update{ID: 100, Kind: coordinator.UpdateMessage, ActorID: 1, ConversationKind: "private"}},
		{name: "message with callback id", update: coordinator.Update{ID: 100, Kind: coordinator.UpdateMessage, CallbackQueryID: "query"}},
		{name: "message with source message", update: coordinator.Update{ID: 100, Kind: coordinator.UpdateMessage, SourceMessageID: 9}},
		{name: "callback without query id", update: coordinator.Update{ID: 100, Kind: coordinator.UpdateCallback, SourceMessageID: 9}},
		{name: "callback without source message", update: coordinator.Update{ID: 100, Kind: coordinator.UpdateCallback, CallbackQueryID: "query"}},
		{name: "unknown enum", update: coordinator.Update{ID: 100, Kind: coordinator.UpdateKind("edited")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStoreWith(coordinator.Checkpoint{Initialized: true, NextUpdateID: 100})
			handler := &fakeHandler{}
			source := &fakeSource{batches: [][]coordinator.Update{{test.update}}}
			err := newLoop(t, source, store, handler, &fakeSender{}, &fakeReadiness{}).Run(context.Background())
			if !errors.Is(err, coordinator.ErrInvalidUpdateSequence) {
				t.Fatalf("Run() error = %v, want ErrInvalidUpdateSequence", err)
			}
			if handler.callCount() != 0 || store.saveCount() != 0 {
				t.Fatalf("invalid update reached handler or state: calls/saves = %d/%d", handler.callCount(), store.saveCount())
			}
		})
	}
}

func TestRejectsNonAscendingAndOverflowUpdateIDsBeforeHandling(t *testing.T) {
	tests := []struct {
		name    string
		updates []coordinator.Update
	}{
		{name: "descending", updates: []coordinator.Update{{ID: 61}, {ID: 60}}},
		{name: "duplicate", updates: []coordinator.Update{{ID: 61}, {ID: 61}}},
		{name: "below checkpoint", updates: []coordinator.Update{{ID: 59}}},
		{name: "overflow", updates: []coordinator.Update{{ID: int64(^uint64(0) >> 1)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStoreWith(coordinator.Checkpoint{Initialized: true, NextUpdateID: 60})
			handler := &fakeHandler{}
			source := &fakeSource{batches: [][]coordinator.Update{test.updates}}
			err := newLoop(t, source, store, handler, &fakeSender{}, &fakeReadiness{}).Run(context.Background())
			if !errors.Is(err, coordinator.ErrInvalidUpdateSequence) {
				t.Fatalf("Run() error = %v, want ErrInvalidUpdateSequence", err)
			}
			if handler.callCount() != 0 || store.checkpoint().NextUpdateID != 60 {
				t.Fatalf("invalid batch mutated state or reached handler")
			}
		})
	}
}

func TestCheckpointRereadMismatchAfterIdentityGatePreventsPollingAndSend(t *testing.T) {
	store := newMemoryStore()
	store.corruptReread = true
	ready := &fakeReadiness{}
	sender := &fakeSender{}
	source := &fakeSource{bootstrapNext: 70}
	err := newLoop(t, source, store, &fakeHandler{}, sender, ready).Run(context.Background())
	if !errors.Is(err, coordinator.ErrCheckpointMismatch) {
		t.Fatalf("Run() error = %v, want ErrCheckpointMismatch", err)
	}
	if ready.callCount() != 1 || source.pollCalls != 0 || sender.callCount() != 0 {
		t.Fatalf(
			"ready/poll/send after mismatched reread = %d/%d/%d, want 1/0/0",
			ready.callCount(), source.pollCalls, sender.callCount(),
		)
	}
}

func newLoop(
	t *testing.T,
	source coordinator.Source,
	store coordinator.CheckpointStore,
	handler coordinator.Handler,
	sender coordinator.Sender,
	readiness coordinator.Readiness,
) *coordinator.Loop {
	t.Helper()
	loop, err := coordinator.NewLoop(source, store, handler, sender, readiness)
	if err != nil {
		t.Fatalf("NewLoop() error = %v", err)
	}
	return loop
}

type fakeSource struct {
	bootstrapNext  int64
	bootstrapCalls int
	batches        [][]coordinator.Update
	pollOffsets    []int64
	pollCalls      int
	pollErr        error
	beforePoll     func(int64)
}

func (source *fakeSource) Bootstrap(context.Context) (int64, error) {
	source.bootstrapCalls++
	return source.bootstrapNext, nil
}

func (source *fakeSource) Poll(_ context.Context, next int64) ([]coordinator.Update, error) {
	source.pollCalls++
	source.pollOffsets = append(source.pollOffsets, next)
	if source.beforePoll != nil {
		source.beforePoll(next)
	}
	if len(source.batches) > 0 {
		batch := source.batches[0]
		source.batches = source.batches[1:]
		return batch, nil
	}
	return nil, source.pollErr
}

type fakeHandler struct {
	mu        sync.Mutex
	decisions map[int64]coordinator.Decision
	called    []coordinator.Update
}

type recoveryHandler struct {
	fakeHandler
	unknown       int64
	recoveryCalls int
}

func (handler *recoveryHandler) Handle(_ context.Context, update coordinator.Update) (coordinator.Decision, error) {
	if update.ID == handler.unknown {
		return coordinator.Decision{}, errors.New("callback effect is unknown")
	}
	return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
}

func (handler *recoveryHandler) PrepareUnknownRecovery(_ context.Context, update coordinator.Update) (coordinator.RecoveryControl, coordinator.Decision, error) {
	handler.recoveryCalls++
	if update.ID != handler.unknown {
		return coordinator.RecoveryControl{}, coordinator.Decision{}, errors.New("wrong unknown update")
	}
	return coordinator.RecoveryControl{OriginalOperationID: "status:20", PromptOperationID: "recovery:20", UpdateID: update.ID}, coordinator.Decision{
		Kind:   coordinator.DecisionStatus,
		Status: coordinator.Status{ConversationID: update.ConversationID, Text: "operator recovery required"},
	}, nil
}

func (handler *fakeHandler) Handle(_ context.Context, update coordinator.Update) (coordinator.Decision, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.called = append(handler.called, update)
	return handler.decisions[update.ID], nil
}

func (handler *fakeHandler) callCount() int {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return len(handler.called)
}

func (handler *fakeHandler) ids() []int64 {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	ids := make([]int64, 0, len(handler.called))
	for _, update := range handler.called {
		ids = append(ids, update.ID)
	}
	return ids
}

func (handler *fakeHandler) updates() []coordinator.Update {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]coordinator.Update(nil), handler.called...)
}

type fakeSender struct {
	mu         sync.Mutex
	receipt    coordinator.Receipt
	err        error
	calls      int
	beforeSend func(string, coordinator.Status)
}

type fakeDurableSender struct {
	fakeSender
	enqueueCalls    int
	resolved        bool
	resolvedReceipt coordinator.Receipt
}

func (sender *fakeDurableSender) ResolveStatusReceipt(_ context.Context, _ string) (coordinator.Receipt, bool, error) {
	return sender.resolvedReceipt, sender.resolved, nil
}

func (sender *fakeDurableSender) EnqueueStatus(_ context.Context, operationID string, _ coordinator.Status, _ *coordinator.KeyboardMarkup) (coordinator.DurableOutboundReceipt, error) {
	sender.enqueueCalls++
	return coordinator.DurableOutboundReceipt{OperationID: operationID, Sequence: uint64(sender.enqueueCalls)}, nil
}

func (sender *fakeSender) SendStatus(_ context.Context, operationID string, status coordinator.Status) (coordinator.Receipt, error) {
	if sender.beforeSend != nil {
		sender.beforeSend(operationID, status)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	sender.calls++
	return sender.receipt, sender.err
}

func (sender *fakeSender) callCount() int {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return sender.calls
}

type fakeReadiness struct {
	mu    sync.Mutex
	calls int
	check func(coordinator.Checkpoint)
	err   error
}

func (ready *fakeReadiness) Ready(_ context.Context, checkpoint coordinator.Checkpoint) error {
	if ready.check != nil {
		ready.check(checkpoint)
	}
	ready.mu.Lock()
	defer ready.mu.Unlock()
	ready.calls++
	return ready.err
}

func (ready *fakeReadiness) callCount() int {
	ready.mu.Lock()
	defer ready.mu.Unlock()
	return ready.calls
}

type memoryStore struct {
	mu            sync.Mutex
	stored        coordinator.StoredCheckpoint
	found         bool
	loads         int
	saves         int
	corruptReread bool
}

func newMemoryStore() *memoryStore { return &memoryStore{} }

func newMemoryStoreWith(checkpoint coordinator.Checkpoint) *memoryStore {
	return &memoryStore{stored: coordinator.StoredCheckpoint{Revision: 1, Checkpoint: checkpoint}, found: true}
}

func (store *memoryStore) Load(context.Context) (coordinator.StoredCheckpoint, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.loads++
	result := cloneStored(store.stored)
	if store.corruptReread && store.loads > 1 {
		result.Checkpoint.NextUpdateID++
	}
	return result, store.found, nil
}

func (store *memoryStore) Save(_ context.Context, expectedRevision uint64, next coordinator.Checkpoint) (coordinator.StoredCheckpoint, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.saves++
	if store.found && store.stored.Revision != expectedRevision {
		return coordinator.StoredCheckpoint{}, errors.New("revision conflict")
	}
	if !store.found && expectedRevision != 0 {
		return coordinator.StoredCheckpoint{}, errors.New("revision conflict")
	}
	store.stored = coordinator.StoredCheckpoint{Revision: expectedRevision + 1, Checkpoint: cloneCheckpoint(next)}
	store.found = true
	return cloneStored(store.stored), nil
}

func (store *memoryStore) saveCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saves
}

func (store *memoryStore) checkpoint() coordinator.Checkpoint {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneCheckpoint(store.stored.Checkpoint)
}

func (store *memoryStore) loadCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.loads
}

func cloneStored(stored coordinator.StoredCheckpoint) coordinator.StoredCheckpoint {
	stored.Checkpoint = cloneCheckpoint(stored.Checkpoint)
	return stored
}

func cloneCheckpoint(checkpoint coordinator.Checkpoint) coordinator.Checkpoint {
	if checkpoint.Blocked != nil {
		blocked := *checkpoint.Blocked
		checkpoint.Blocked = &blocked
	}
	if checkpoint.Outbound != nil {
		outbound := *checkpoint.Outbound
		if outbound.Keyboard != nil {
			keyboard := make(coordinator.KeyboardMarkup, len(*outbound.Keyboard))
			for row := range *outbound.Keyboard {
				keyboard[row] = append([]coordinator.KeyboardButton(nil), (*outbound.Keyboard)[row]...)
			}
			outbound.Keyboard = &keyboard
		}
		if outbound.Receipt != nil {
			receipt := *outbound.Receipt
			outbound.Receipt = &receipt
		}
		if outbound.Durable != nil {
			durable := *outbound.Durable
			outbound.Durable = &durable
		}
		checkpoint.Outbound = &outbound
	}
	if checkpoint.Recovery != nil {
		recovery := *checkpoint.Recovery
		checkpoint.Recovery = &recovery
	}
	return checkpoint
}
