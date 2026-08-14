package inbound

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type AppleSpeechConfig struct {
	FFmpegBinary      string
	SpeechBinary      string
	Language          string
	ConvertTimeout    time.Duration
	TranscribeTimeout time.Duration
	MaxOutputBytes    int64
	TempDir           string
	Runner            CommandRunner
}

type AppleSpeechTranscriber struct{ config AppleSpeechConfig }

func NewAppleSpeechTranscriber(config AppleSpeechConfig) (*AppleSpeechTranscriber, error) {
	if config.FFmpegBinary == "" {
		config.FFmpegBinary = "ffmpeg"
	}
	if config.SpeechBinary == "" {
		config.SpeechBinary = "bria-apple-speech"
	}
	if config.Language == "" {
		config.Language = "auto"
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
	if strings.TrimSpace(config.FFmpegBinary) == "" ||
		strings.TrimSpace(config.SpeechBinary) == "" ||
		strings.TrimSpace(config.Language) == "" || config.ConvertTimeout < 1 ||
		config.TranscribeTimeout < 1 || config.MaxOutputBytes < 1 ||
		config.MaxOutputBytes > maximumMaxOutputBytes {
		return nil, fmt.Errorf("%w: invalid Apple Speech configuration", ErrInvalidInput)
	}
	return &AppleSpeechTranscriber{config: config}, nil
}

func (t *AppleSpeechTranscriber) Transcribe(ctx context.Context, audioPath string) (string, error) {
	return t.TranscribeLanguage(ctx, audioPath, t.config.Language)
}

func (t *AppleSpeechTranscriber) TranscribeLanguage(
	ctx context.Context,
	audioPath string,
	language string,
) (string, error) {
	if err := validateRegularFile(audioPath); err != nil {
		return "", fmt.Errorf("inspect voice input: %w", err)
	}
	wav, err := os.CreateTemp(t.config.TempDir, "bria-apple-speech-*.wav")
	if err != nil {
		return "", fmt.Errorf("create Apple Speech temporary file: %w", err)
	}
	wavPath := wav.Name()
	if err := wav.Close(); err != nil {
		os.Remove(wavPath)
		return "", fmt.Errorf("close Apple Speech temporary file: %w", err)
	}
	defer os.Remove(wavPath)
	if err := convertToWAV(
		ctx, t.config.Runner, t.config.FFmpegBinary,
		t.config.ConvertTimeout, audioPath, wavPath,
	); err != nil {
		return "", err
	}
	return t.recognize(ctx, wavPath, language)
}

func (t *AppleSpeechTranscriber) recognize(
	ctx context.Context,
	wavPath string,
	language string,
) (string, error) {
	language = strings.TrimSpace(language)
	if !validAppleSpeechLanguage(language) {
		return "", fmt.Errorf("%w: unsupported Apple Speech language", ErrInvalidInput)
	}
	commandCtx, cancel := context.WithTimeout(ctx, t.config.TranscribeTimeout)
	defer cancel()
	stdout := &truncatingBuffer{limit: int(t.config.MaxOutputBytes) + 1}
	stderr := &truncatingBuffer{limit: 4096}
	err := t.config.Runner.Run(
		commandCtx, stdout, stderr, t.config.SpeechBinary,
		"--input", wavPath, "--locale", language, "--on-device",
	)
	if err != nil {
		return "", commandFailure("Apple Speech", err, stderr.String())
	}
	if int64(len(stdout.data)) > t.config.MaxOutputBytes {
		return "", ErrTranscriptionTooLarge
	}
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		return "", errors.New("Apple Speech returned an empty transcription")
	}
	return text, nil
}

func validAppleSpeechLanguage(language string) bool {
	if language == "" || len(language) > 35 {
		return false
	}
	for _, value := range language {
		if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
			(value >= '0' && value <= '9') || value == '-' {
			continue
		}
		return false
	}
	return true
}
