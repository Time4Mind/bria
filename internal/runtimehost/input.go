package runtimehost

import (
	"context"
	"errors"
	"strings"
	"time"
)

type InputKind string

const (
	InputPhoto    InputKind = "photo"
	InputDocument InputKind = "document"
	InputVoice    InputKind = "voice"
)

// InputFile identifies media held by an external transport. It deliberately
// carries no bytes or credentials; the owning node resolves it directly.
type InputFile struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	UniqueID string `json:"unique_id"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type InputPayload struct {
	Kind          InputKind `json:"kind"`
	Caption       string    `json:"caption,omitempty"`
	Origin        string    `json:"origin,omitempty"`
	VoiceBackend  string    `json:"voice_backend,omitempty"`
	VoiceLanguage string    `json:"voice_language,omitempty"`
	File          InputFile `json:"file"`
}

const maxInputCaptionBytes = 16 << 10

func (p InputPayload) validate() error {
	if p.Kind != InputPhoto && p.Kind != InputDocument && p.Kind != InputVoice {
		return errors.New("unsupported input kind")
	}
	if p.File.Provider != "telegram" || strings.TrimSpace(p.File.ID) == "" ||
		strings.TrimSpace(p.File.UniqueID) == "" {
		return errors.New("external input file identity is invalid")
	}
	if p.File.Size < 0 || len(p.File.ID) > 512 || len(p.File.UniqueID) > 256 ||
		len(p.File.Name) > 255 || len(p.File.MIMEType) > 255 ||
		len(p.Caption) > maxInputCaptionBytes || len(p.Origin) > 512 {
		return errors.New("external input metadata exceeds limits")
	}
	if p.Kind != InputVoice && (p.VoiceBackend != "" || p.VoiceLanguage != "") {
		return errors.New("voice metadata is valid only for voice input")
	}
	if p.Kind == InputVoice && p.VoiceBackend != "" && p.VoiceBackend != "auto" &&
		p.VoiceBackend != "whisper" && p.VoiceBackend != "apple" && p.VoiceBackend != "off" {
		return errors.New("unsupported voice backend")
	}
	if p.Kind == InputVoice && p.VoiceLanguage != "" && p.VoiceLanguage != "auto" &&
		p.VoiceLanguage != "ru" && p.VoiceLanguage != "en" && p.VoiceLanguage != "zh" {
		return errors.New("unsupported voice language")
	}
	return nil
}

type InputResolver interface {
	ResolveInput(context.Context, string, InputPayload) (string, error)
}

// InputResolveTiming is optional diagnostic data produced by an origin-node
// resolver. It contains durations only; input text, file identity, paths, and
// provider credentials must never be copied into it.
type InputResolveTiming struct {
	Download   time.Duration
	Transcribe time.Duration
}

// TimedInputResolver preserves the transport-neutral InputResolver contract
// while allowing implementations to expose bounded phase timings. Executors
// fall back to aggregate resolve timing when this interface is unavailable.
type TimedInputResolver interface {
	ResolveInputWithTiming(
		context.Context,
		string,
		InputPayload,
	) (string, InputResolveTiming, error)
}
