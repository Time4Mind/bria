package inbound

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type commandCall struct {
	name string
	args []string
}

type whisperRunner struct {
	calls      []commandCall
	text       string
	wavPath    string
	wait       bool
	stdoutOnly bool
}

func TestWhisperDefaultAllowsSlowCPUNodes(t *testing.T) {
	transcriber, err := NewWhisperTranscriber(WhisperConfig{ModelPath: "/model"})
	if err != nil {
		t.Fatal(err)
	}
	if transcriber.config.TranscribeTimeout != 10*time.Minute {
		t.Fatalf("default transcription timeout = %s", transcriber.config.TranscribeTimeout)
	}
}

func (r *whisperRunner) Run(
	ctx context.Context,
	stdout io.Writer,
	_ io.Writer,
	name string,
	args ...string,
) error {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if name == "custom-ffmpeg" {
		r.wavPath = args[len(args)-1]
		if r.wait {
			<-ctx.Done()
			return ctx.Err()
		}
		return os.WriteFile(r.wavPath, []byte("wav"), 0o600)
	}
	wavIndex := slices.Index(args, "-f")
	if wavIndex < 0 || wavIndex+1 >= len(args) {
		return errors.New("missing wav argument")
	}
	r.wavPath = args[wavIndex+1]
	if r.stdoutOnly {
		_, err := io.WriteString(stdout, r.text)
		return err
	}
	return os.WriteFile(r.wavPath+".txt", []byte(r.text), 0o600)
}

func TestWhisperTranscriberRunsExactCommandsAndCleansTemps(t *testing.T) {
	tempDir := t.TempDir()
	audio := filepath.Join(tempDir, "voice;still-an-argument.ogg")
	model := filepath.Join(tempDir, "model.bin")
	writeInboundTestFile(t, audio, "ogg")
	writeInboundTestFile(t, model, "model")
	runner := &whisperRunner{text: "  распознанный текст  "}
	transcriber, err := NewWhisperTranscriber(WhisperConfig{
		FFmpegBinary: "custom-ffmpeg", WhisperBinary: "custom-whisper",
		ModelPath: model, Language: "ru", Threads: 6, TempDir: tempDir, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := transcriber.Transcribe(context.Background(), audio)
	if err != nil {
		t.Fatal(err)
	}
	if text != "распознанный текст" || len(runner.calls) != 2 {
		t.Fatalf("text=%q calls=%#v", text, runner.calls)
	}
	ffmpeg := runner.calls[0]
	if ffmpeg.name != "custom-ffmpeg" || !slices.Contains(ffmpeg.args, audio) {
		t.Fatalf("unexpected ffmpeg call: %#v", ffmpeg)
	}
	whisper := runner.calls[1]
	if whisper.name != "custom-whisper" ||
		!containsAdjacent(whisper.args, "-m", model) ||
		!containsAdjacent(whisper.args, "-l", "ru") ||
		!containsAdjacent(whisper.args, "-t", "6") {
		t.Fatalf("unexpected whisper call: %#v", whisper)
	}
	if _, err := os.Stat(runner.wavPath); !os.IsNotExist(err) {
		t.Fatalf("wav temporary file remains: %v", err)
	}
	if _, err := os.Stat(runner.wavPath + ".txt"); !os.IsNotExist(err) {
		t.Fatalf("text temporary file remains: %v", err)
	}
}

func TestWhisperTranscriberBoundsOutput(t *testing.T) {
	tempDir := t.TempDir()
	audio := filepath.Join(tempDir, "voice.ogg")
	model := filepath.Join(tempDir, "model.bin")
	writeInboundTestFile(t, audio, "ogg")
	writeInboundTestFile(t, model, "model")
	runner := &whisperRunner{text: strings.Repeat("x", 9)}
	transcriber, err := NewWhisperTranscriber(WhisperConfig{
		FFmpegBinary: "custom-ffmpeg", WhisperBinary: "custom-whisper",
		ModelPath: model, MaxOutputBytes: 8, TempDir: tempDir, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = transcriber.Transcribe(context.Background(), audio)
	if !errors.Is(err, ErrTranscriptionTooLarge) {
		t.Fatalf("got %v, want transcription too large", err)
	}
	if _, err := os.Stat(runner.wavPath); !os.IsNotExist(err) {
		t.Fatalf("wav temporary file remains: %v", err)
	}
}

func TestWhisperTranscriberHonorsStageTimeout(t *testing.T) {
	tempDir := t.TempDir()
	audio := filepath.Join(tempDir, "voice.ogg")
	model := filepath.Join(tempDir, "model.bin")
	writeInboundTestFile(t, audio, "ogg")
	writeInboundTestFile(t, model, "model")
	runner := &whisperRunner{wait: true}
	transcriber, err := NewWhisperTranscriber(WhisperConfig{
		FFmpegBinary: "custom-ffmpeg", WhisperBinary: "custom-whisper",
		ModelPath: model, ConvertTimeout: time.Millisecond,
		TranscribeTimeout: time.Second, TempDir: tempDir, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = transcriber.Transcribe(context.Background(), audio)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
	if _, statErr := os.Stat(runner.wavPath); !os.IsNotExist(statErr) {
		t.Fatalf("wav temporary file remains: %v", statErr)
	}
}

func TestWhisperTranscriberFallsBackToBoundedStdout(t *testing.T) {
	tempDir := t.TempDir()
	audio := filepath.Join(tempDir, "voice.ogg")
	model := filepath.Join(tempDir, "model.bin")
	writeInboundTestFile(t, audio, "ogg")
	writeInboundTestFile(t, model, "model")
	runner := &whisperRunner{text: " stdout result ", stdoutOnly: true}
	transcriber, err := NewWhisperTranscriber(WhisperConfig{
		FFmpegBinary: "custom-ffmpeg", WhisperBinary: "custom-whisper",
		ModelPath: model, TempDir: tempDir, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := transcriber.Transcribe(context.Background(), audio)
	if err != nil || text != "stdout result" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func containsAdjacent(values []string, key, value string) bool {
	index := slices.Index(values, key)
	return index >= 0 && index+1 < len(values) && values[index+1] == value
}

func writeInboundTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
