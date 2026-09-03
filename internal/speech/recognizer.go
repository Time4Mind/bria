// Package speech defines the transport-neutral local speech recognition port.
package speech

import (
	"context"
	"errors"
)

var (
	ErrInvalidConfiguration = errors.New("speech recognizer configuration is invalid")
	ErrInvalidAudio         = errors.New("speech audio input is invalid")
	ErrTranscriptTooLarge   = errors.New("speech transcript exceeds size limit")
	ErrRecognitionFailed    = errors.New("speech recognition failed")
)

// Recognizer transcribes one service-owned local audio file.
type Recognizer interface {
	Transcribe(context.Context, string) (string, error)
}
