package parakeet_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/speech"
	"bria/internal/speech/parakeet"
)

func TestCommandReturnsBoundedTrimmedTranscriptWithoutShell(t *testing.T) {
	directory := t.TempDir()
	audioPath := filepath.Join(directory, "voice $(must-not-run).ogg")
	if err := os.WriteFile(audioPath, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := helperCommand(t, "success", 64)

	transcript, err := command.Transcribe(context.Background(), audioPath)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if transcript != "hello from parakeet" {
		t.Fatalf("transcript = %q", transcript)
	}
	if _, err := os.Lstat(filepath.Join(directory, "must-not-run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audio path was interpreted by a shell: %v", err)
	}
}

func TestCommandInsertsModelPathAndAppendsAudioAsSeparateArguments(t *testing.T) {
	directory := t.TempDir()
	modelPath := filepath.Join(directory, "model $(must-not-run).bin")
	if err := os.WriteFile(modelPath, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	audioPath := filepath.Join(directory, "audio $(must-not-run).ogg")
	if err := os.WriteFile(audioPath, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(directory, "arguments.txt")
	command := helperCommand(t, "arguments", 64)
	command.ModelPath = modelPath
	command.Arguments = []string{"--before", parakeet.ModelPathPlaceholder, "--after"}
	command.Environment = append(command.Environment, "PARAKEET_ARGUMENTS_RECEIPT="+receiptPath)

	if _, err := command.Transcribe(context.Background(), audioPath); err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	got, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"--before", modelPath, "--after", audioPath}, "\n")
	if string(got) != want {
		t.Fatalf("argv = %q, want %q", string(got), want)
	}
	if _, err := os.Lstat(filepath.Join(directory, "must-not-run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("model or audio path was interpreted by a shell: %v", err)
	}
}

func TestCommandRejectsOversizeTranscript(t *testing.T) {
	audioPath := regularAudioFile(t)
	command := helperCommand(t, "oversize", 4)

	_, err := command.Transcribe(context.Background(), audioPath)
	if !errors.Is(err, speech.ErrTranscriptTooLarge) {
		t.Fatalf("error = %v, want ErrTranscriptTooLarge", err)
	}
}

func TestCommandSanitizesProcessFailure(t *testing.T) {
	audioPath := regularAudioFile(t)
	command := helperCommand(t, "failure", 64)

	_, err := command.Transcribe(context.Background(), audioPath)
	if !errors.Is(err, speech.ErrRecognitionFailed) {
		t.Fatalf("error = %v, want ErrRecognitionFailed", err)
	}
	message := err.Error()
	for _, secret := range []string{audioPath, command.ModelPath, "provider-secret", command.Executable} {
		if strings.Contains(message, secret) {
			t.Fatalf("error leaks %q: %q", secret, message)
		}
	}
}

func TestCommandRejectsSymlinkAudioAndImplicitEnvironment(t *testing.T) {
	audioPath := regularAudioFile(t)
	link := audioPath + ".link"
	if err := os.Symlink(audioPath, link); err != nil {
		t.Fatal(err)
	}
	command := helperCommand(t, "success", 64)
	command.Environment = nil
	if _, err := command.Transcribe(context.Background(), audioPath); !errors.Is(err, speech.ErrInvalidConfiguration) {
		t.Fatalf("nil environment error = %v", err)
	}
	command.Environment = []string{}
	if _, err := command.Transcribe(context.Background(), link); !errors.Is(err, speech.ErrInvalidAudio) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestCommandRejectsMissingOrDuplicateModelPlaceholder(t *testing.T) {
	audioPath := regularAudioFile(t)
	for name, arguments := range map[string][]string{
		"missing":        {"--model", "model.bin"},
		"embedded":       {"--model={model_path}"},
		"duplicate":      {parakeet.ModelPathPlaceholder, parakeet.ModelPathPlaceholder},
		"case sensitive": {"{MODEL_PATH}"},
	} {
		t.Run(name, func(t *testing.T) {
			command := helperCommand(t, "success", 64)
			command.Arguments = arguments
			_, err := command.Transcribe(context.Background(), audioPath)
			if !errors.Is(err, speech.ErrInvalidConfiguration) {
				t.Fatalf("Transcribe() error = %v, want ErrInvalidConfiguration", err)
			}
		})
	}
}

func TestCommandRejectsInvalidOrUnavailableModelWithoutLeakingPath(t *testing.T) {
	directory := t.TempDir()
	regularPath := filepath.Join(directory, "model.bin")
	if err := os.WriteFile(regularPath, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(directory, "model.link")
	if err := os.Symlink(regularPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	for name, modelPath := range map[string]string{
		"relative": "model.bin",
		"unclean":  directory + "/models/../model.bin",
		"NUL":      regularPath + "\x00secret",
		"symlink":  symlinkPath,
		"missing":  filepath.Join(directory, "missing-secret-model.bin"),
	} {
		t.Run(name, func(t *testing.T) {
			command := helperCommand(t, "success", 64)
			command.ModelPath = modelPath
			_, err := command.Transcribe(context.Background(), regularAudioFile(t))
			if !errors.Is(err, speech.ErrInvalidConfiguration) {
				t.Fatalf("Transcribe() error = %v, want ErrInvalidConfiguration", err)
			}
			if strings.Contains(err.Error(), modelPath) {
				t.Fatalf("Transcribe() error leaked model path %q: %q", modelPath, err)
			}
		})
	}
}

func TestMain(m *testing.M) {
	mode := os.Getenv("PARAKEET_HELPER_MODE")
	if mode == "" {
		os.Exit(m.Run())
	}
	if len(os.Args) < 2 {
		os.Exit(7)
	}
	audioPath := os.Args[len(os.Args)-1]
	if !filepath.IsAbs(audioPath) {
		os.Exit(7)
	}
	switch mode {
	case "success":
		fmt.Print("  hello from parakeet\n")
	case "oversize":
		fmt.Print("12345")
	case "failure":
		fmt.Fprintf(os.Stderr, "provider-secret at %s", audioPath)
		os.Exit(9)
	case "arguments":
		receiptPath := os.Getenv("PARAKEET_ARGUMENTS_RECEIPT")
		if receiptPath == "" {
			os.Exit(8)
		}
		if err := os.WriteFile(receiptPath, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
			os.Exit(8)
		}
		fmt.Print("ok")
	default:
		os.Exit(8)
	}
	os.Exit(0)
}

func helperCommand(t *testing.T, mode string, maxTranscript int64) parakeet.Command {
	t.Helper()
	return parakeet.Command{
		Executable:         os.Args[0],
		ModelPath:          regularModelFile(t),
		Arguments:          []string{parakeet.ModelPathPlaceholder},
		Environment:        []string{"PARAKEET_HELPER_MODE=" + mode},
		MaxTranscriptBytes: maxTranscript,
		MaxDiagnosticBytes: 32,
	}
}

func regularModelFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(path, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func regularAudioFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audio.ogg")
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
