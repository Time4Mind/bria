package telegram_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/telegram"
)

type schedulerClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

type blockingSchedulerClock struct {
	mu      sync.Mutex
	now     time.Time
	started chan struct{}
	release chan struct{}
}

func (clock *blockingSchedulerClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *blockingSchedulerClock) Wait(ctx context.Context, delay time.Duration) error {
	clock.started <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-clock.release:
		clock.mu.Lock()
		clock.now = clock.now.Add(delay)
		clock.mu.Unlock()
		return nil
	}
}

func (clock *schedulerClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *schedulerClock) Wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	clock.mu.Lock()
	clock.waits = append(clock.waits, delay)
	clock.now = clock.now.Add(delay)
	clock.mu.Unlock()
	return nil
}

func (clock *schedulerClock) Advance(delay time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delay)
	clock.mu.Unlock()
}

func TestMutationSchedulerUsesStagedCardCadenceAndResetsOnCardSwitch(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	options := telegram.SchedulerOptions{StatePath: filepath.Join(t.TempDir(), "scheduler.json"), Now: clock.Now, Wait: clock.Wait, Jitter: func(time.Duration) time.Duration { return 0 }}
	scheduler, err := telegram.OpenMutationScheduler(options)
	if err != nil {
		t.Fatal(err)
	}
	mutation := telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 10}
	for attempt := 1; attempt <= 31; attempt++ {
		lease, err := scheduler.Acquire(context.Background(), mutation)
		if err != nil {
			t.Fatalf("Acquire(%d): %v", attempt, err)
		}
		if err := lease.Complete(telegram.MutationOutcome{}); err != nil {
			t.Fatalf("Complete(%d): %v", attempt, err)
		}
	}
	if len(clock.waits) != 30 {
		t.Fatalf("wait count = %d, want 30", len(clock.waits))
	}
	for index, delay := range clock.waits {
		want := 1500 * time.Millisecond
		if index >= 9 && index < 29 {
			want = 2500 * time.Millisecond
		} else if index >= 29 {
			want = 3500 * time.Millisecond
		}
		if delay != want {
			t.Fatalf("wait[%d] = %v, want %v", index, delay, want)
		}
	}

	before := len(clock.waits)
	lease, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 11})
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.waits[before:]; len(got) != 1 || got[0] != 40*time.Millisecond {
		t.Fatalf("card switch waits = %v, want only 40ms global spacing", got)
	}
	_ = lease.Complete(telegram.MutationOutcome{})
}

func TestMutationSchedulerUsesFiveSecondCardCadenceAfterThirtyMinutes(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
	if err != nil {
		t.Fatal(err)
	}
	mutation := telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 10}
	first, err := scheduler.Acquire(context.Background(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Complete(telegram.MutationOutcome{})
	clock.Advance(30 * time.Minute)
	second, err := scheduler.Acquire(context.Background(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Complete(telegram.MutationOutcome{})
	third, err := scheduler.Acquire(context.Background(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 5*time.Second {
		t.Fatalf("thirty-minute cadence wait = %v, want 5s", got)
	}
	_ = third.Complete(telegram.MutationOutcome{})
}

func TestMutationSchedulerResetsLongCardCadenceAfterInteractiveSessionSwitch(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
	if err != nil {
		t.Fatal(err)
	}
	cardA := telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 10}
	lease, err := scheduler.Acquire(context.Background(), cardA)
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Complete(telegram.MutationOutcome{})
	clock.Advance(30 * time.Minute)
	lease, err = scheduler.Acquire(context.Background(), cardA)
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Complete(telegram.MutationOutcome{})
	for _, cardID := range []int64{11, 10} {
		lease, err = scheduler.Acquire(context.Background(), telegram.Mutation{
			Method: "editMessageText", ChatID: 42, CardID: cardID, Priority: telegram.MutationInteractive,
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = lease.Complete(telegram.MutationOutcome{})
	}
	lease, err = scheduler.Acquire(context.Background(), cardA)
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Complete(telegram.MutationOutcome{})
	lease, err = scheduler.Acquire(context.Background(), cardA)
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 1500*time.Millisecond {
		t.Fatalf("cadence after switch back = %v, want reset 1.5s", got)
	}
	_ = lease.Complete(telegram.MutationOutcome{})
}

func TestMutationSchedulerDoesNotApplyCardCadenceToOtherMutations(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []telegram.Mutation{
		{Method: "sendMessage", ChatID: 42},
		{Method: "deleteMessage", ChatID: 42},
		{Method: "sendDocument", ChatID: 42, Heavy: true},
	} {
		lease, acquireErr := scheduler.Acquire(context.Background(), mutation)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		_ = lease.Complete(telegram.MutationOutcome{})
	}
	for index, delay := range clock.waits {
		if delay != 40*time.Millisecond {
			t.Fatalf("non-card wait[%d] = %v, want only global 40ms spacing", index, delay)
		}
	}
}

func TestMutationSchedulerPersistsCooldownAndLastMutation(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	path := filepath.Join(t.TempDir(), "scheduler.json")
	options := telegram.SchedulerOptions{StatePath: path, Now: clock.Now, Wait: clock.Wait, Jitter: func(time.Duration) time.Duration { return 0 }}
	scheduler, err := telegram.OpenMutationScheduler(options)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "sendMessage", ChatID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(telegram.MutationOutcome{HTTPStatus: http.StatusTooManyRequests, ErrorCode: 429, RetryAfter: 7 * time.Second}); err != nil {
		t.Fatal(err)
	}

	reopened, err := telegram.OpenMutationScheduler(options)
	if err != nil {
		t.Fatal(err)
	}
	lease, err = reopened.Acquire(context.Background(), telegram.Mutation{Method: "sendMessage", ChatID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 7*time.Second {
		t.Fatalf("restored cooldown wait = %v, want 7s", got)
	}
	if err := lease.Complete(telegram.MutationOutcome{}); err != nil {
		t.Fatal(err)
	}
}

func TestMutationSchedulerAllowsOnlyOneProbeAfterSharedCooldown(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait, Jitter: func(time.Duration) time.Duration { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Complete(telegram.MutationOutcome{HTTPStatus: 429, ErrorCode: 429, RetryAfter: 4 * time.Second}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(4 * time.Second)

	probe, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "deleteMessage", ChatID: 42})
	if err != nil {
		t.Fatal(err)
	}
	type acquireResult struct {
		lease *telegram.MutationLease
		err   error
	}
	queued := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "sendMessage", ChatID: 43})
		queued <- acquireResult{lease: lease, err: acquireErr}
	}()
	select {
	case result := <-queued:
		t.Fatalf("second mutation escaped before probe completion: %#v", result)
	case <-time.After(20 * time.Millisecond):
	}
	if err := probe.Complete(telegram.MutationOutcome{}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-queued:
		if result.err != nil || result.lease == nil {
			t.Fatalf("queued mutation after probe = %#v", result)
		}
		_ = result.lease.Complete(telegram.MutationOutcome{})
	case <-time.After(time.Second):
		t.Fatal("queued mutation did not resume after successful probe")
	}
}

func TestMutationSchedulerCapsServerBackoffAtFifteenSeconds(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	options := telegram.SchedulerOptions{StatePath: filepath.Join(t.TempDir(), "scheduler.json"), Now: clock.Now, Wait: clock.Wait, Jitter: func(time.Duration) time.Duration { return 0 }}
	scheduler, err := telegram.OpenMutationScheduler(options)
	if err != nil {
		t.Fatal(err)
	}
	mutation := telegram.Mutation{Method: "editMessageText"}
	want := []time.Duration{4 * time.Second, 8 * time.Second, 12 * time.Second, 15 * time.Second, 15 * time.Second}
	lease, err := scheduler.Acquire(context.Background(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	for index, delay := range want {
		if err := lease.Complete(telegram.MutationOutcome{HTTPStatus: http.StatusBadGateway, ErrorCode: 502}); err != nil {
			t.Fatal(err)
		}
		lease, err = scheduler.Acquire(context.Background(), mutation)
		if err != nil {
			t.Fatal(err)
		}
		if got := clock.waits[len(clock.waits)-1]; got != delay {
			t.Fatalf("backoff[%d] = %v, want %v", index, got, delay)
		}
	}
	if _, err := telegram.OpenMutationScheduler(options); err != nil {
		t.Fatalf("reopen after capped failures: %v", err)
	}
	_ = lease.Complete(telegram.MutationOutcome{})
}

func TestMutationSchedulerAllowsOnlyOneHeavyOperationInFlight(t *testing.T) {
	t.Parallel()

	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "sendDocument", Heavy: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := scheduler.Acquire(ctx, telegram.Mutation{Method: "sendPhoto", Heavy: true}); err == nil {
		t.Fatal("second heavy Acquire() succeeded while first remained in flight")
	}
	_ = first.Complete(telegram.MutationOutcome{})
}

func TestMutationSchedulerSupersedesOnlyTheOlderWaitingCardState(t *testing.T) {
	t.Parallel()

	clock := &blockingSchedulerClock{
		now: time.Unix(1_700_000_000, 0).UTC(), started: make(chan struct{}, 2), release: make(chan struct{}),
	}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
	if err != nil {
		t.Fatal(err)
	}
	mutation := telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 10}
	first, err := scheduler.Acquire(context.Background(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Complete(telegram.MutationOutcome{})

	type result struct {
		lease *telegram.MutationLease
		err   error
	}
	older := make(chan result, 1)
	newer := make(chan result, 1)
	go func() {
		lease, err := scheduler.Acquire(context.Background(), mutation)
		older <- result{lease: lease, err: err}
	}()
	<-clock.started
	go func() {
		lease, err := scheduler.Acquire(context.Background(), mutation)
		newer <- result{lease: lease, err: err}
	}()
	<-clock.started
	close(clock.release)

	oldResult := <-older
	if !errors.Is(oldResult.err, telegram.ErrMutationSuperseded) {
		t.Fatalf("older Acquire() error = %v, want superseded", oldResult.err)
	}
	newResult := <-newer
	if newResult.err != nil || newResult.lease == nil {
		t.Fatalf("newer Acquire() = %#v, %v", newResult.lease, newResult.err)
	}
	_ = newResult.lease.Complete(telegram.MutationOutcome{})
}

func TestMutationSchedulerReservesGlobalCapacityForInteractiveWork(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 23; index++ {
		lease, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "deleteMessage"})
		if err != nil {
			t.Fatal(err)
		}
		_ = lease.Complete(telegram.MutationOutcome{})
	}
	interactive, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "answerCallbackQuery", Priority: telegram.MutationInteractive})
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 40*time.Millisecond {
		t.Fatalf("interactive spacing wait = %v, want 40ms", got)
	}
	_ = interactive.Complete(telegram.MutationOutcome{})
	card, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 7, Priority: telegram.MutationInteractive})
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 40*time.Millisecond {
		t.Fatalf("interactive card spacing wait = %v, want 40ms", got)
	}
	_ = card.Complete(telegram.MutationOutcome{})
	routine, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "deleteMessage"})
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 40*time.Millisecond {
		t.Fatalf("regular post-reservation wait = %v, want 40ms", got)
	}
	_ = routine.Complete(telegram.MutationOutcome{})
}

func TestMutationSchedulerSpacesGlobalStartsAndReservesRoutineCapacity(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 24; index++ {
		lease, acquireErr := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "answerCallbackQuery", Priority: telegram.MutationInteractive})
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		_ = lease.Complete(telegram.MutationOutcome{})
	}
	for index, delay := range clock.waits {
		if delay != 40*time.Millisecond {
			t.Fatalf("global spacing wait[%d] = %v, want 40ms", index, delay)
		}
	}
	routine, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "deleteMessage"})
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 40*time.Millisecond {
		t.Fatalf("reserved routine wait = %v, want 40ms", got)
	}
	_ = routine.Complete(telegram.MutationOutcome{})
	interactive, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "answerCallbackQuery", Priority: telegram.MutationInteractive})
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 40*time.Millisecond {
		t.Fatalf("post-window interactive spacing = %v, want 40ms", got)
	}
	_ = interactive.Complete(telegram.MutationOutcome{})
}

func TestMutationSchedulerReportsOnlyBoundedSafeDiagnostics(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	var diagnostics []telegram.SchedulerDiagnostic
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{
		Now: clock.Now, Wait: clock.Wait, Jitter: func(time.Duration) time.Duration { return 0 },
		Report: func(diagnostic telegram.SchedulerDiagnostic) { diagnostics = append(diagnostics, diagnostic) },
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "editMessageText", ChatID: 42, CardID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(telegram.MutationOutcome{HTTPStatus: 429, ErrorCode: 429, RetryAfter: 6 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	got := diagnostics[0]
	if got.Method != "editMessageText" || got.ErrorClass != "rate_limited" || got.RetryAfter != 6*time.Second || got.CooldownUntil != clock.now.Add(6*time.Second) {
		t.Fatalf("diagnostic = %#v", got)
	}
}

func TestMutationSchedulerStopsUnauthorizedMutationPool(t *testing.T) {
	t.Parallel()

	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "sendMessage", ChatID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(telegram.MutationOutcome{HTTPStatus: http.StatusUnauthorized, ErrorCode: 401}); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Acquire(context.Background(), telegram.Mutation{Method: "sendMessage", ChatID: 42}); !errors.Is(err, telegram.ErrMutationStopped) {
		t.Fatalf("Acquire() after 401 error = %v, want stopped", err)
	}
}

func TestScheduledClientRepairsGlobal401OnlyAfterSuccessfulGetMe(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mustTestClient(t, "200006:test-only", httpClientFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch filepath.Base(request.URL.Path) {
		case "sendMessage":
			if calls == 1 {
				return jsonResponse(http.StatusUnauthorized, `{"ok":false,"error_code":401}`), nil
			}
			return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":9,"from":{"id":2,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"done"}}`), nil
		case "getMe":
			return jsonResponse(http.StatusOK, `{"ok":true,"result":{"id":2,"is_bot":true,"username":"bria_test_bot"}}`), nil
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
			return nil, nil
		}
	}), telegram.Options{Scheduler: scheduler})
	request := telegram.SendMessageRequest{ChatID: 42, Text: "done"}
	if _, err := client.SendMessage(context.Background(), request); err == nil {
		t.Fatal("initial unauthorized send error = nil")
	}
	if _, err := client.SendMessage(context.Background(), request); !errors.Is(err, telegram.ErrMutationStopped) || telegram.IsDeliveryUnknown(err) {
		t.Fatalf("blocked send error = %v, want stopped", err)
	}
	if calls != 1 {
		t.Fatalf("blocked mutation reached HTTP: calls=%d", calls)
	}
	if _, err := client.GetMe(context.Background()); err != nil {
		t.Fatalf("GetMe() repair error = %v", err)
	}
	if message, err := client.SendMessage(context.Background(), request); err != nil || message.MessageID != 9 {
		t.Fatalf("send after repair = (%#v, %v)", message, err)
	}
	if calls != 3 {
		t.Fatalf("HTTP calls after repair = %d, want 3", calls)
	}
}

func TestScheduledClientScopes403StopToRejectedRecipient(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mustTestClient(t, "200007:test-only", httpClientFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResponse(http.StatusForbidden, `{"ok":false,"error_code":403}`), nil
		}
		return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":10,"from":{"id":2,"is_bot":true},"chat":{"id":43,"type":"private"},"text":"done"}}`), nil
	}), telegram.Options{Scheduler: scheduler})
	if _, err := client.SendMessage(context.Background(), telegram.SendMessageRequest{ChatID: 42, Text: "done"}); err == nil {
		t.Fatal("initial forbidden send error = nil")
	}
	if _, err := client.SendMessage(context.Background(), telegram.SendMessageRequest{ChatID: 42, Text: "done"}); !errors.Is(err, telegram.ErrMutationStopped) || telegram.IsDeliveryUnknown(err) {
		t.Fatalf("blocked recipient error = %v, want stopped", err)
	}
	message, err := client.SendMessage(context.Background(), telegram.SendMessageRequest{ChatID: 43, Text: "done"})
	if err != nil || message.MessageID != 10 || calls != 2 {
		t.Fatalf("other recipient send = (%#v, %v), calls=%d", message, err, calls)
	}
}

func TestScheduledClientRetriesExplicit429ButNotAmbiguousCreation(t *testing.T) {
	t.Parallel()

	t.Run("explicit 429", func(t *testing.T) {
		clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
		scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait, Jitter: func(time.Duration) time.Duration { return 0 }})
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		client := mustTestClient(t, "200001:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return jsonResponse(http.StatusTooManyRequests, `{"ok":false,"error_code":429,"parameters":{"retry_after":6}}`), nil
			}
			return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":9,"from":{"id":2,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"done"}}`), nil
		}), telegram.Options{Scheduler: scheduler})
		message, err := client.SendMessage(context.Background(), telegram.SendMessageRequest{ChatID: 42, Text: "done"})
		if err != nil || message.MessageID != 9 || calls != 2 {
			t.Fatalf("SendMessage() = %#v, %v; calls=%d", message, err, calls)
		}
		if got := clock.waits[len(clock.waits)-1]; got != 6*time.Second {
			t.Fatalf("429 wait = %v, want 6s", got)
		}
	})

	t.Run("ambiguous server creation", func(t *testing.T) {
		clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
		scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		client := mustTestClient(t, "200002:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusBadGateway, `{"ok":false,"error_code":502}`), nil
		}), telegram.Options{Scheduler: scheduler})
		_, err = client.SendMessage(context.Background(), telegram.SendMessageRequest{ChatID: 42, Text: "done"})
		if !telegram.IsDeliveryUnknown(err) || calls != 1 {
			t.Fatalf("SendMessage() error/calls = %v/%d, want DeliveryUnknown/1", err, calls)
		}
	})

	t.Run("invalid successful creation receipt", func(t *testing.T) {
		clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
		scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		client := mustTestClient(t, "200004:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":0}}`), nil
		}), telegram.Options{Scheduler: scheduler})
		_, err = client.SendMessage(context.Background(), telegram.SendMessageRequest{ChatID: 42, Text: "done"})
		if !telegram.IsDeliveryUnknown(err) || calls != 1 {
			t.Fatalf("SendMessage() error/calls = %v/%d, want DeliveryUnknown/1", err, calls)
		}
	})
}

func TestScheduledClientConvergesIdempotentEditAndDelete(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait, Jitter: func(time.Duration) time.Duration { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	editCalls := 0
	client := mustTestClient(t, "200003:test-only", httpClientFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case filepath.Base(request.URL.Path) == "editMessageText":
			editCalls++
			if editCalls == 1 {
				return jsonResponse(http.StatusBadGateway, `{"ok":false,"error_code":502}`), nil
			}
			return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":7,"from":{"id":2,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"latest"}}`), nil
		case filepath.Base(request.URL.Path) == "deleteMessage":
			return jsonResponse(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: message to delete not found"}`), nil
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
			return nil, nil
		}
	}), telegram.Options{Scheduler: scheduler})
	message, err := client.EditMessageText(context.Background(), telegram.EditMessageTextRequest{ChatID: 42, MessageID: 7, Text: "latest"})
	if err != nil || message.MessageID != 7 || editCalls != 2 {
		t.Fatalf("EditMessageText() = %#v, %v; calls=%d", message, err, editCalls)
	}
	if err := client.DeleteMessage(context.Background(), telegram.DeleteMessageRequest{ChatID: 42, MessageID: 8}); err != nil {
		t.Fatalf("DeleteMessage(already absent) error = %v", err)
	}
}

func TestScheduledClientTreatsMessageNotModifiedAsConfirmed(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mustTestClient(t, "200008:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`), nil
	}), telegram.Options{Scheduler: scheduler})
	message, err := client.EditMessageText(context.Background(), telegram.EditMessageTextRequest{ChatID: 42, MessageID: 7, Text: "same"})
	if err != nil || message.MessageID != 7 || message.Chat.ID != 42 || calls != 1 {
		t.Fatalf("not-modified edit = (%#v, %v), calls=%d", message, err, calls)
	}
}

func TestScheduledClientRetriesExplicitUpload429OnceCooldownAllows(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait, Jitter: func(time.Duration) time.Duration { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mustTestClient(t, "200009:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResponse(http.StatusTooManyRequests, `{"ok":false,"error_code":429,"parameters":{"retry_after":5}}`), nil
		}
		return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":11,"from":{"id":2,"is_bot":true},"chat":{"id":42,"type":"private"},"document":{"file_id":"file","file_unique_id":"unique"}}}`), nil
	}), telegram.Options{Scheduler: scheduler})
	receipt, err := client.SendDocument(context.Background(), telegram.SendDocumentRequest{
		ChatID: 42, FileName: "report.txt", ContentType: "text/plain", Content: []byte("result"),
	})
	if err != nil || receipt.MessageID != 11 || calls != 2 {
		t.Fatalf("upload after 429 = (%#v, %v), calls=%d", receipt, err, calls)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 5*time.Second {
		t.Fatalf("upload 429 wait = %v, want 5s", got)
	}
}

func TestScheduledClientRetriesInvalidEditReceiptAsLatestState(t *testing.T) {
	t.Parallel()

	clock := &schedulerClock{now: time.Unix(1_700_000_000, 0).UTC()}
	scheduler, err := telegram.OpenMutationScheduler(telegram.SchedulerOptions{Now: clock.Now, Wait: clock.Wait, Jitter: func(time.Duration) time.Duration { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mustTestClient(t, "200005:test-only", httpClientFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":0}}`), nil
		}
		return jsonResponse(http.StatusOK, `{"ok":true,"result":{"message_id":7,"from":{"id":2,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"latest"}}`), nil
	}), telegram.Options{Scheduler: scheduler})
	message, err := client.EditMessageText(context.Background(), telegram.EditMessageTextRequest{ChatID: 42, MessageID: 7, Text: "latest"})
	if err != nil || message.MessageID != 7 || calls != 2 {
		t.Fatalf("EditMessageText() = %#v, %v; calls=%d", message, err, calls)
	}
	if got := clock.waits[len(clock.waits)-1]; got != 4*time.Second {
		t.Fatalf("invalid receipt retry wait = %v, want 4s", got)
	}
}
