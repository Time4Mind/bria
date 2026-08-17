package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/inbound"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type inputDownloaderStub struct{ data string }

func (d inputDownloaderStub) Download(
	_ context.Context, _ string, destination io.Writer, _ int64,
) (int64, error) {
	written, err := io.WriteString(destination, d.data)
	return int64(written), err
}

type inputTranscriberStub struct{ text string }

func (t inputTranscriberStub) Transcribe(context.Context, string) (string, error) {
	return t.text, nil
}

func TestConfiguredTranscriberRestrictsAppleSpeechToMacOS(t *testing.T) {
	configured, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	configured.SpeechEngine = config.SpeechEngineApple
	if _, err := configuredTranscriber(configured, t.TempDir(), "linux"); err == nil || !strings.Contains(err.Error(), "macOS") {
		t.Fatalf("non-macOS Apple Speech error=%v", err)
	}
	if _, err := configuredTranscriber(configured, t.TempDir(), "darwin"); err != nil {
		t.Fatalf("macOS Apple Speech configuration rejected: %v", err)
	}
}

func TestConfiguredSpeechEngineIsNotOverriddenOnMacOS(t *testing.T) {
	configured, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	configured.SpeechEngine = config.SpeechEngineWhisper
	if got := configuredDefaultVoice(configured); got != config.SpeechEngineWhisper {
		t.Fatalf("configured speech engine=%q", got)
	}
}

func TestRuntimeInputResolverSelectsRequestedVoiceBackend(t *testing.T) {
	resolver := runtimeInputResolver{
		downloader: inputDownloaderStub{data: "voice"},
		transcribers: map[string]inbound.Transcriber{
			"whisper": inputTranscriberStub{text: "whisper text"},
			"apple":   inputTranscriberStub{text: "apple text"},
		},
		defaultVoice: "whisper", maxDownloadSize: 1024, temporary: t.TempDir(),
	}
	payload := runtimehost.InputPayload{
		Kind: runtimehost.InputVoice, VoiceBackend: "apple",
		File: runtimehost.InputFile{Provider: "telegram", ID: "voice", UniqueID: "unique"},
	}
	got, err := resolver.ResolveInput(context.Background(), t.TempDir(), payload)
	if err != nil || got != "apple text" {
		t.Fatalf("selected Apple Speech=%q, %v", got, err)
	}
	payload.VoiceBackend = "auto"
	got, err = resolver.ResolveInput(context.Background(), t.TempDir(), payload)
	if err != nil || got != "whisper text" {
		t.Fatalf("automatic voice backend=%q, %v", got, err)
	}
	payload.VoiceBackend = "off"
	if _, err := resolver.ResolveInput(context.Background(), t.TempDir(), payload); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled voice backend error=%v", err)
	}
}
