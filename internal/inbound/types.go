// Package inbound processes Telegram input media on the node that owns the
// target session. It intentionally has no dependency on cluster or runtime
// packages; adapters pass only normalized metadata and receive local paths or
// text suitable for a later runtime payload.
package inbound

import (
	"context"
	"errors"
	"io"
)

type Kind string

const (
	KindText     Kind = "text"
	KindPhoto    Kind = "photo"
	KindDocument Kind = "document"
	KindVoice    Kind = "voice"
)

type Input struct {
	Kind     Kind
	Text     string
	FileID   string
	UniqueID string
	FileName string
	MIMEType string
	Size     int64
	Language string
}

type Result struct {
	Kind         Kind
	Text         string
	Caption      string
	RelativePath string
}

// Downloader streams one Telegram file directly to dst. Implementations must
// honor maxBytes; Processor independently enforces the same limit at Writer.
type Downloader interface {
	Download(ctx context.Context, fileID string, dst io.Writer, maxBytes int64) (int64, error)
}

// Transcriber consumes a local audio file and returns plain recognized text.
type Transcriber interface {
	Transcribe(ctx context.Context, audioPath string) (string, error)
}

// LanguageTranscriber allows a transport-selected language to override the
// node default for one recording. Implementations still receive a closed,
// validated language set from the runtime boundary.
type LanguageTranscriber interface {
	TranscribeLanguage(ctx context.Context, audioPath, language string) (string, error)
}

type ProcessorConfig struct {
	Downloader       Downloader
	Transcriber      Transcriber
	MaxDownloadBytes int64
	TempDir          string
}

var (
	ErrInvalidInput          = errors.New("invalid inbound input")
	ErrMediaTooLarge         = errors.New("inbound media exceeds size limit")
	ErrUnsafePath            = errors.New("unsafe inbound path")
	ErrTranscriptionTooLarge = errors.New("transcription exceeds size limit")
)

const defaultMaxDownloadBytes = 20 << 20
