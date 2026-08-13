package inbound

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type appleSpeechRunner struct {
	calls   []commandCall
	wavPath string
}

func (r *appleSpeechRunner) Run(
	_ context.Context,
	stdout io.Writer,
	_ io.Writer,
	name string,
	args ...string,
) error {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if name == "custom-ffmpeg" {
		r.wavPath = args[len(args)-1]
		return os.WriteFile(r.wavPath, []byte("wav"), 0o600)
	}
	_, err := io.WriteString(stdout, "  native transcript  ")
	return err
}

func TestAppleSpeechTranscriberUsesOnDeviceHelperAndCleansWAV(t *testing.T) {
	temporary := t.TempDir()
	audio := filepath.Join(temporary, "voice.ogg")
	writeInboundTestFile(t, audio, "ogg")
	runner := &appleSpeechRunner{}
	transcriber, err := NewAppleSpeechTranscriber(AppleSpeechConfig{
		FFmpegBinary: "custom-ffmpeg", SpeechBinary: "custom-apple-speech",
		Language: "ru-RU", TempDir: temporary, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := transcriber.Transcribe(context.Background(), audio)
	if err != nil || text != "native transcript" || len(runner.calls) != 2 {
		t.Fatalf("text=%q calls=%#v err=%v", text, runner.calls, err)
	}
	call := runner.calls[1]
	if call.name != "custom-apple-speech" ||
		!containsAdjacent(call.args, "--input", runner.wavPath) ||
		!containsAdjacent(call.args, "--locale", "ru-RU") ||
		!slices.Contains(call.args, "--on-device") {
		t.Fatalf("Apple Speech call=%#v", call)
	}
	if _, err := os.Stat(runner.wavPath); !os.IsNotExist(err) {
		t.Fatalf("temporary WAV remains: %v", err)
	}
}
