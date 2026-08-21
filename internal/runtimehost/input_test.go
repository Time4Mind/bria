package runtimehost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type inputResolverStub struct {
	workdir string
	input   InputPayload
	text    string
	started chan struct{}
	release chan struct{}
}

type timedInputResolverStub struct {
	text   string
	timing InputResolveTiming
	step   func()
}

func (r timedInputResolverStub) ResolveInput(
	context.Context,
	string,
	InputPayload,
) (string, error) {
	return r.text, nil
}

func (r timedInputResolverStub) ResolveInputWithTiming(
	context.Context,
	string,
	InputPayload,
) (string, InputResolveTiming, error) {
	if r.step != nil {
		r.step()
	}
	return r.text, r.timing, nil
}

func (r *inputResolverStub) ResolveInput(
	_ context.Context,
	workdir string,
	input InputPayload,
) (string, error) {
	r.workdir, r.input = workdir, input
	if r.started != nil {
		close(r.started)
		<-r.release
	}
	return r.text, nil
}

func TestExternalInputResolvesInsideSessionFIFO(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	resolver := &inputResolverStub{text: "inspect .bria-inbox/photo.jpg"}
	executor.SetInputResolver(resolver)
	request := testRequest("photo-1", ActionSendInput)
	request.Input = &InputPayload{
		Kind: InputPhoto, Caption: "inspect",
		File: InputFile{Provider: "telegram", ID: "file-id", UniqueID: "unique-id"},
	}
	result := waitSubmittedResult(t, executor, request)
	if !result.Delivered || result.ResolvedText != resolver.text {
		t.Fatalf("result=%+v", result)
	}
	if resolver.input.File.ID != "file-id" {
		t.Fatalf("resolved input=%+v", resolver.input)
	}
	calls := driver.snapshot()
	if len(calls) != 1 || calls[0].value != resolver.text {
		t.Fatalf("driver calls=%#v", calls)
	}
}

func TestVoiceResolutionKeepsLaterTextBehindIt(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	resolver := &inputResolverStub{
		text: "recognized voice", started: make(chan struct{}), release: make(chan struct{}),
	}
	executor.SetInputResolver(resolver)
	voice := testRequest("voice-first", ActionSendInput)
	voice.Input = &InputPayload{
		Kind: InputVoice,
		File: InputFile{Provider: "telegram", ID: "voice", UniqueID: "voice-unique"},
	}
	text := testRequest("text-second", ActionSendInput)
	text.Text = "typed later"
	if _, err := executor.Submit(context.Background(), voice); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Submit(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	<-resolver.started
	if calls := driver.snapshot(); len(calls) != 0 {
		t.Fatalf("later input bypassed voice: %#v", calls)
	}
	close(resolver.release)
	waitResult(t, executor, voice.OperationID)
	waitResult(t, executor, text.OperationID)
	calls := driver.snapshot()
	if len(calls) != 2 || calls[0].value != "recognized voice" || calls[1].value != "typed later" {
		t.Fatalf("driver order=%#v", calls)
	}
}

func TestExternalInputRejectsMissingTelegramIdentity(t *testing.T) {
	request := testRequest("photo-invalid", ActionSendInput)
	request.Input = &InputPayload{Kind: InputPhoto}
	if err := request.validate(); err == nil {
		t.Fatal("invalid external input was accepted")
	}
}

func TestVoiceBackendMetadataIsClosedAndVoiceOnly(t *testing.T) {
	valid := InputPayload{
		Kind: InputVoice, VoiceBackend: "apple", VoiceLanguage: "ru",
		File: InputFile{Provider: "telegram", ID: "voice", UniqueID: "unique"},
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid voice backend rejected: %v", err)
	}
	invalid := valid
	invalid.VoiceBackend = "remote"
	if err := invalid.validate(); err == nil {
		t.Fatal("unknown voice backend accepted")
	}
	invalid = valid
	invalid.Kind = InputPhoto
	if err := invalid.validate(); err == nil {
		t.Fatal("voice metadata accepted for photo")
	}
	invalid = valid
	invalid.VoiceLanguage = "arbitrary"
	if err := invalid.validate(); err == nil {
		t.Fatal("unknown voice language accepted")
	}
}

func TestInputDeliveryTimingSeparatesAttachmentFIFOResolveAndTmux(t *testing.T) {
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := base
	executor := &LocalExecutor{ctx: context.Background(), now: func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}}
	session := &localSession{
		binding:  RuntimeBinding{NodeID: "node-a", SessionID: "session-a", Generation: 4},
		executor: executor,
	}
	session.wake = sync.NewCond(&session.mu)
	request := testRequest("voice-timing", ActionSendInput)
	request.Input = &InputPayload{Kind: InputVoice}
	if !session.enqueue(request, base) {
		t.Fatal("request was not queued")
	}
	mu.Lock()
	now = base.Add(3 * time.Second)
	mu.Unlock()
	if !session.attach(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4, TmuxTarget: "@7",
	}) {
		t.Fatal("runtime was not attached")
	}
	mu.Lock()
	now = base.Add(5 * time.Second)
	mu.Unlock()
	gotRequest, binding, queue, ok := session.next()
	if !ok || gotRequest.OperationID != request.OperationID || binding.TmuxTarget != "@7" {
		t.Fatalf("dequeue ok=%t request=%+v binding=%+v", ok, gotRequest, binding)
	}
	if queue.queue != 5*time.Second || queue.attachmentWait != 3*time.Second || queue.fifoWait != 2*time.Second {
		t.Fatalf("queue timing=%+v", queue)
	}

	delivery := newInputDeliveryTiming(
		request, binding, queue,
		inputExecutionTiming{
			resolve: 2100 * time.Millisecond, download: 600 * time.Millisecond,
			transcribe: 1200 * time.Millisecond, tmuxSend: 400 * time.Millisecond,
		},
		Result{Delivered: true}, nil, base.Add(7500*time.Millisecond),
	)
	if delivery.total != 7500*time.Millisecond || delivery.prepare != 300*time.Millisecond ||
		delivery.outcome != "delivered" || delivery.kind != "voice" {
		t.Fatalf("delivery timing=%+v", delivery)
	}
}

func TestInputDeliveryTimingEmitsOncePerQueuedOperation(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	timings := make(chan inputDeliveryTiming, 2)
	executor.inputTiming = func(timing inputDeliveryTiming) { timings <- timing }
	request := testRequest("voice-observed-once", ActionSendInput)
	request.Input = &InputPayload{
		Kind: InputVoice,
		File: InputFile{Provider: "telegram", ID: "voice", UniqueID: "voice-unique"},
	}
	executor.SetInputResolver(timedInputResolverStub{
		text: "recognized", timing: InputResolveTiming{Download: time.Millisecond, Transcribe: 2 * time.Millisecond},
	})
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := executor.Submit(context.Background(), request); err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	waitResult(t, executor, request.OperationID)
	select {
	case timing := <-timings:
		if timing.operationID != request.OperationID || timing.ref != "node-a/session-a" ||
			timing.generation != 4 || timing.kind != "voice" || timing.outcome != "delivered" {
			t.Fatalf("timing=%+v", timing)
		}
	case <-time.After(time.Second):
		t.Fatal("input timing was not emitted")
	}
	select {
	case duplicate := <-timings:
		t.Fatalf("duplicate timing=%+v", duplicate)
	default:
	}
}

func TestInputDeliveryTimingTerminatesWhenQueuedRuntimeIsUnregistered(t *testing.T) {
	executor, err := NewLocalExecutor("node-a", &fakeRuntimeDriver{}, NewMemoryOperationStore())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := executor.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	if err := executor.Prepare(RuntimeBinding{
		NodeID: "node-a", SessionID: "session-a", Generation: 4, Backend: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	timings := make(chan inputDeliveryTiming, 1)
	executor.inputTiming = func(timing inputDeliveryTiming) { timings <- timing }
	request := testRequest("voice-unregistered", ActionSendInput)
	request.Input = &InputPayload{
		Kind: InputVoice,
		File: InputFile{Provider: "telegram", ID: "voice", UniqueID: "voice-unique"},
	}
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := executor.Unregister("node-a", "session-a", 4); err != nil {
		t.Fatal(err)
	}
	_, found, lookupErr := executor.LookupResult(context.Background(), request.OperationID)
	if !found || !errors.Is(lookupErr, ErrRuntimeUnavailable) {
		t.Fatalf("found=%t err=%v", found, lookupErr)
	}
	select {
	case timing := <-timings:
		if timing.operationID != request.OperationID || timing.outcome != "runtime_unavailable" ||
			timing.kind != "voice" {
			t.Fatalf("timing=%+v", timing)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal input timing was not emitted")
	}
}
