package runtimehost

import (
	"context"
	"testing"
)

type inputResolverStub struct {
	workdir string
	input   InputPayload
	text    string
	started chan struct{}
	release chan struct{}
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
		Kind: InputVoice, VoiceBackend: "apple",
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
		t.Fatal("voice backend accepted for photo")
	}
}
