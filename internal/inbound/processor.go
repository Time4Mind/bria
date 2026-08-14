package inbound

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type Processor struct {
	downloader  Downloader
	transcriber Transcriber
	maxBytes    int64
	tempDir     string
}

func NewProcessor(config ProcessorConfig) (*Processor, error) {
	if config.Downloader == nil {
		return nil, fmt.Errorf("%w: downloader is required", ErrInvalidInput)
	}
	if config.MaxDownloadBytes == 0 {
		config.MaxDownloadBytes = defaultMaxDownloadBytes
	}
	if config.MaxDownloadBytes < 1 {
		return nil, fmt.Errorf("%w: download limit must be positive", ErrInvalidInput)
	}
	return &Processor{
		downloader: config.Downloader, transcriber: config.Transcriber,
		maxBytes: config.MaxDownloadBytes, tempDir: config.TempDir,
	}, nil
}

func (p *Processor) Process(ctx context.Context, workdir string, input Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !filepath.IsAbs(workdir) || strings.ContainsRune(workdir, 0) {
		return Result{}, fmt.Errorf("%w: workdir must be absolute", ErrInvalidInput)
	}
	switch input.Kind {
	case KindText:
		return Result{Kind: KindText, Text: strings.TrimSpace(input.Text)}, nil
	case KindPhoto, KindDocument:
		return p.processPersistent(ctx, filepath.Clean(workdir), input)
	case KindVoice:
		return p.processVoice(ctx, input)
	default:
		return Result{}, fmt.Errorf("%w: unsupported kind %q", ErrInvalidInput, input.Kind)
	}
}

func (p *Processor) processPersistent(
	ctx context.Context,
	workdir string,
	input Input,
) (Result, error) {
	if err := validateMediaInput(input, p.maxBytes); err != nil {
		return Result{}, err
	}
	inbox, err := prepareInbox(workdir)
	if err != nil {
		return Result{}, err
	}
	name := stableMediaName(input)
	destination := filepath.Join(inbox, name)
	if exists, err := validExistingMedia(destination); err != nil {
		return Result{}, err
	} else if !exists {
		if err := p.downloadAtomic(ctx, inbox, destination, input); err != nil {
			return Result{}, err
		}
	}
	relative, err := filepath.Rel(workdir, destination)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Result{}, ErrUnsafePath
	}
	return Result{
		Kind: input.Kind, Caption: strings.TrimSpace(input.Text), RelativePath: relative,
	}, nil
}

func (p *Processor) processVoice(ctx context.Context, input Input) (Result, error) {
	if p.transcriber == nil {
		return Result{}, fmt.Errorf("%w: transcriber is required for voice", ErrInvalidInput)
	}
	if err := validateMediaInput(input, p.maxBytes); err != nil {
		return Result{}, err
	}
	file, err := os.CreateTemp(p.tempDir, "bria-voice-*.ogg")
	if err != nil {
		return Result{}, fmt.Errorf("create voice temporary file: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return Result{}, fmt.Errorf("secure voice temporary file: %w", err)
	}
	if err := p.downloadTo(ctx, file, input); err != nil {
		file.Close()
		return Result{}, err
	}
	if err := file.Close(); err != nil {
		return Result{}, fmt.Errorf("close voice temporary file: %w", err)
	}
	var text string
	if transcriber, ok := p.transcriber.(LanguageTranscriber); ok && input.Language != "" {
		text, err = transcriber.TranscribeLanguage(ctx, path, input.Language)
	} else {
		text, err = p.transcriber.Transcribe(ctx, path)
	}
	if err != nil {
		return Result{}, fmt.Errorf("transcribe voice: %w", err)
	}
	text = strings.TrimSpace(text)
	if !containsTranscriptContent(text) {
		return Result{}, errors.New("transcribe voice: recognizer returned no speech")
	}
	return Result{Kind: KindVoice, Text: text}, nil
}

func containsTranscriptContent(text string) bool {
	for _, value := range text {
		if unicode.IsLetter(value) || unicode.IsNumber(value) {
			return true
		}
	}
	return false
}

func validateMediaInput(input Input, maxBytes int64) error {
	if strings.TrimSpace(input.FileID) == "" || strings.TrimSpace(input.UniqueID) == "" {
		return fmt.Errorf("%w: file and unique ids are required", ErrInvalidInput)
	}
	if input.Size < 0 {
		return fmt.Errorf("%w: negative media size", ErrInvalidInput)
	}
	if input.Size > maxBytes {
		return ErrMediaTooLarge
	}
	return nil
}
