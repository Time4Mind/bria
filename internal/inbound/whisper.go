package inbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type WhisperConfig struct {
	FFmpegBinary      string
	WhisperBinary     string
	ModelPath         string
	Language          string
	Threads           int
	ConvertTimeout    time.Duration
	TranscribeTimeout time.Duration
	MaxOutputBytes    int64
	TempDir           string
	Runner            CommandRunner
}

type WhisperTranscriber struct {
	config WhisperConfig
}

const (
	defaultConvertTimeout = 30 * time.Second
	// CPU-only nodes can need several times the recording duration for the
	// medium model, especially while another session is busy. Two minutes made
	// otherwise valid 30-60 second Telegram voice notes fail nondeterministically.
	defaultTranscribeTimeout = 10 * time.Minute
	defaultMaxOutputBytes    = 64 << 10
	maximumMaxOutputBytes    = 1 << 20
)

func NewWhisperTranscriber(config WhisperConfig) (*WhisperTranscriber, error) {
	if config.FFmpegBinary == "" {
		config.FFmpegBinary = "ffmpeg"
	}
	if config.WhisperBinary == "" {
		config.WhisperBinary = "whisper-cli"
	}
	if config.Language == "" {
		config.Language = "auto"
	}
	if config.Threads == 0 {
		config.Threads = 4
	}
	if config.ConvertTimeout == 0 {
		config.ConvertTimeout = defaultConvertTimeout
	}
	if config.TranscribeTimeout == 0 {
		config.TranscribeTimeout = defaultTranscribeTimeout
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = defaultMaxOutputBytes
	}
	if config.Runner == nil {
		config.Runner = ExecCommandRunner{}
	}
	if strings.TrimSpace(config.ModelPath) == "" || config.Threads < 1 ||
		config.ConvertTimeout < 1 || config.TranscribeTimeout < 1 || config.MaxOutputBytes < 1 ||
		config.MaxOutputBytes > maximumMaxOutputBytes {
		return nil, fmt.Errorf("%w: invalid whisper configuration", ErrInvalidInput)
	}
	return &WhisperTranscriber{config: config}, nil
}

func (t *WhisperTranscriber) Transcribe(ctx context.Context, audioPath string) (string, error) {
	return t.TranscribeLanguage(ctx, audioPath, t.config.Language)
}

func (t *WhisperTranscriber) TranscribeLanguage(
	ctx context.Context,
	audioPath string,
	language string,
) (string, error) {
	if err := validateRegularFile(audioPath); err != nil {
		return "", fmt.Errorf("inspect voice input: %w", err)
	}
	if err := validateRegularFile(t.config.ModelPath); err != nil {
		return "", fmt.Errorf("inspect whisper model: %w", err)
	}
	wav, err := os.CreateTemp(t.config.TempDir, "bria-whisper-*.wav")
	if err != nil {
		return "", fmt.Errorf("create whisper temporary file: %w", err)
	}
	wavPath := wav.Name()
	if err := wav.Close(); err != nil {
		os.Remove(wavPath)
		return "", fmt.Errorf("close whisper temporary file: %w", err)
	}
	textPath := wavPath + ".txt"
	defer os.Remove(wavPath)
	defer os.Remove(textPath)

	if err := t.convert(ctx, audioPath, wavPath); err != nil {
		return "", err
	}
	stdout, err := t.runWhisper(ctx, wavPath, language)
	if err != nil {
		return "", err
	}
	text, err := readBoundedText(textPath, t.config.MaxOutputBytes)
	if errors.Is(err, os.ErrNotExist) {
		text = strings.TrimSpace(stdout)
		if int64(len(text)) > t.config.MaxOutputBytes {
			return "", ErrTranscriptionTooLarge
		}
		return text, nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func (t *WhisperTranscriber) convert(ctx context.Context, input, output string) error {
	return convertToWAV(
		ctx, t.config.Runner, t.config.FFmpegBinary, t.config.ConvertTimeout, input, output,
	)
}

func (t *WhisperTranscriber) runWhisper(
	ctx context.Context,
	wavPath string,
	language string,
) (string, error) {
	language = strings.ToLower(strings.TrimSpace(language))
	if language != "auto" && language != "ru" && language != "en" && language != "zh" {
		return "", fmt.Errorf("%w: unsupported whisper language", ErrInvalidInput)
	}
	commandCtx, cancel := context.WithTimeout(ctx, t.config.TranscribeTimeout)
	defer cancel()
	stdout := &truncatingBuffer{limit: int(t.config.MaxOutputBytes) + 1}
	stderr := &truncatingBuffer{limit: 4096}
	err := t.config.Runner.Run(commandCtx, stdout, stderr, t.config.WhisperBinary,
		"-m", t.config.ModelPath, "-f", wavPath, "-nt", "-otxt",
		"-t", strconv.Itoa(t.config.Threads), "-l", language,
	)
	if err != nil {
		return "", commandFailure("whisper-cli", err, stderr.String())
	}
	return stdout.String(), nil
}

func commandFailure(stage string, err error, detail string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return fmt.Errorf("%s: %w", stage, err)
	}
	return fmt.Errorf("%s: %w: %s", stage, err, detail)
}

func validateRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafePath
	}
	return nil
}

func readBoundedText(path string, limit int64) (string, error) {
	if err := validateRegularFile(path); err != nil {
		return "", err
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", fmt.Errorf("read whisper output: %w", err)
	}
	if int64(len(data)) > limit {
		return "", ErrTranscriptionTooLarge
	}
	return string(data), nil
}
