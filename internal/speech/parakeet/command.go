// Package parakeet adapts a local Parakeet command to the speech port.
package parakeet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"bria/internal/speech"
)

// ModelPathPlaceholder reserves one argument position for the configured
// model path. It is deliberately not inferred from flags or environment.
const ModelPathPlaceholder = "{model_path}"

// Command configures a direct process invocation. Arguments are trusted static
// configuration; the model placeholder and audio path are each passed as one
// separate argument.
type Command struct {
	Executable         string
	ModelPath          string
	Arguments          []string
	Environment        []string
	WorkingDirectory   string
	MaxTranscriptBytes int64
	MaxDiagnosticBytes int64
}

var _ speech.Recognizer = Command{}

// Transcribe runs local Parakeet without a shell.
func (c Command) Transcribe(ctx context.Context, audioPath string) (string, error) {
	if err := c.validate(); err != nil {
		return "", speech.ErrInvalidConfiguration
	}
	model, err := os.Lstat(c.ModelPath)
	if err != nil || model.Mode()&os.ModeSymlink != 0 || !model.Mode().IsRegular() {
		return "", speech.ErrInvalidConfiguration
	}
	if ctx == nil || !filepath.IsAbs(audioPath) || strings.ContainsRune(audioPath, 0) {
		return "", speech.ErrInvalidAudio
	}
	info, err := os.Lstat(audioPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", speech.ErrInvalidAudio
	}

	arguments := make([]string, 0, len(c.Arguments)+1)
	for _, argument := range c.Arguments {
		if argument == ModelPathPlaceholder {
			arguments = append(arguments, c.ModelPath)
			continue
		}
		arguments = append(arguments, argument)
	}
	arguments = append(arguments, audioPath)
	command := exec.CommandContext(ctx, c.Executable, arguments...)
	command.Env = append([]string{}, c.Environment...)
	command.Dir = c.WorkingDirectory
	transcript := &limitedBuffer{limit: c.MaxTranscriptBytes}
	diagnostic := &limitedBuffer{limit: c.MaxDiagnosticBytes, discardOverflow: true}
	command.Stdout = transcript
	command.Stderr = diagnostic
	runErr := command.Run()
	if transcript.exceeded {
		return "", speech.ErrTranscriptTooLarge
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if runErr != nil {
		return "", speech.ErrRecognitionFailed
	}
	result := strings.TrimSpace(transcript.buffer.String())
	if result == "" || !utf8.ValidString(result) || strings.ContainsRune(result, 0) {
		return "", speech.ErrRecognitionFailed
	}
	return result, nil
}

func (c Command) validate() error {
	if !filepath.IsAbs(c.Executable) || strings.ContainsRune(c.Executable, 0) || c.Environment == nil || c.MaxTranscriptBytes <= 0 || c.MaxDiagnosticBytes <= 0 {
		return speech.ErrInvalidConfiguration
	}
	if !filepath.IsAbs(c.ModelPath) || filepath.Clean(c.ModelPath) != c.ModelPath || strings.ContainsRune(c.ModelPath, 0) {
		return speech.ErrInvalidConfiguration
	}
	if c.WorkingDirectory != "" && (!filepath.IsAbs(c.WorkingDirectory) || strings.ContainsRune(c.WorkingDirectory, 0)) {
		return speech.ErrInvalidConfiguration
	}
	modelPlaceholderCount := 0
	for _, argument := range c.Arguments {
		if strings.ContainsRune(argument, 0) {
			return speech.ErrInvalidConfiguration
		}
		if argument == ModelPathPlaceholder {
			modelPlaceholderCount++
		}
	}
	if modelPlaceholderCount != 1 {
		return speech.ErrInvalidConfiguration
	}
	for _, entry := range c.Environment {
		if strings.ContainsRune(entry, 0) || !strings.Contains(entry, "=") {
			return speech.ErrInvalidConfiguration
		}
	}
	return nil
}

type limitedBuffer struct {
	buffer          bytes.Buffer
	limit           int64
	exceeded        bool
	discardOverflow bool
}

func (w *limitedBuffer) Write(value []byte) (int, error) {
	remaining := w.limit - int64(w.buffer.Len())
	if remaining >= int64(len(value)) {
		return w.buffer.Write(value)
	}
	if remaining > 0 {
		_, _ = w.buffer.Write(value[:remaining])
	}
	w.exceeded = true
	if w.discardOverflow {
		return len(value), nil
	}
	return int(max(remaining, 0)), errOutputLimit
}

var errOutputLimit = errors.New("process output limit reached")

var _ io.Writer = (*limitedBuffer)(nil)
