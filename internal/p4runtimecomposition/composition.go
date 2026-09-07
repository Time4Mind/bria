// Package p4runtimecomposition assembles opt-in local media and Screen ports.
package p4runtimecomposition

import (
	"context"
	"errors"
	"fmt"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/inputcomposition"
	"bria/internal/mediaproduction"
	"bria/internal/providerinputcomposition"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/speech/parakeet"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/turnprocessing"
)

const (
	screenMaxSessions   = 256
	screenMaxLines      = 200
	screenMaxColumns    = 240
	screenMaxEventBytes = 64 << 10
	screenMaxPNGBytes   = 10 << 20
)

var ErrInvalidOptions = errors.New("P4 runtime composition is invalid")

type Bundle struct {
	InputPreparer turnprocessing.InputPreparer
	Attachments   turnprocessing.AttachmentCustody
	Submitter     sessionruntime.Submitter
	RuntimeEvents turnprocessing.RuntimeEventObserver
	// Finals is intentionally replaceable: artifact retry routing is owned by
	// a separate composition and must not be fabricated here.
	Finals turnprocessing.FinalProcessor
}

type Options struct {
	Configuration config.Config
	Telegram      *telegram.Client
	Settings      settings.Store
	Sessions      *storage.SessionStore
	Runtime       providerinputcomposition.Runtime
	Finals        turnprocessing.FinalProcessor
}

// Open returns enabled=false when runtime.p4 is absent. It does not infer any
// local path, model command, or media policy.
func Open(options Options) (*Bundle, bool, error) {
	p4, enabled := options.Configuration.P4Runtime()
	if !enabled {
		return nil, false, nil
	}
	if options.Telegram == nil || options.Settings == nil || options.Sessions == nil || options.Runtime == nil ||
		options.Configuration.Parakeet == nil || options.Configuration.MediaLimits == nil {
		return nil, true, ErrInvalidOptions
	}
	limits, command := options.Configuration.MediaLimits, options.Configuration.Parakeet
	if limits.TranscriptBytes > int64(^uint(0)>>1) {
		return nil, true, ErrInvalidOptions
	}
	media, err := mediaproduction.Open(options.Telegram, mediaproduction.Config{
		VoiceTempDirectory: p4.VoiceTempDirectory, PhotoDirectory: p4.PhotoCustodyDirectory,
		VoiceBytes: limits.VoiceBytes, PhotoBytes: limits.PhotoBytes, PreparedBytes: int(limits.TranscriptBytes),
		Parakeet:     parakeet.Command{Executable: command.Executable, ModelPath: command.ModelPath, Arguments: append([]string(nil), command.Argv...), Environment: []string{}, MaxTranscriptBytes: limits.TranscriptBytes, MaxDiagnosticBytes: limits.DiagnosticBytes},
		DocumentMode: mediaproduction.DocumentsReject,
	})
	if err != nil {
		return nil, true, fmt.Errorf("compose media runtime: %w", err)
	}
	inputs, err := inputcomposition.Open(media)
	if err != nil {
		return nil, true, fmt.Errorf("compose media inputs: %w", err)
	}
	var submitter sessionruntime.Submitter
	if current, ok := options.Runtime.(providerinputcomposition.CurrentRuntime); ok {
		submitter, err = providerinputcomposition.NewCurrent(current, media.Photos, sessionProviders{store: options.Sessions})
	} else {
		submitter, err = providerinputcomposition.New(options.Runtime, media.Photos, sessionProviders{store: options.Sessions})
	}
	if err != nil {
		return nil, true, fmt.Errorf("compose attachment provider router: %w", err)
	}
	// Screen is an active-card attachment from the native cache, not an
	// inference-event observer or a producer of independent background photos.
	return &Bundle{InputPreparer: inputs, Attachments: inputs, Submitter: submitter, Finals: options.Finals}, true, nil
}

type sessionProviders struct{ store *storage.SessionStore }

func (providers sessionProviders) ProviderForSession(ctx context.Context, id domain.SessionID) (domain.Provider, error) {
	if providers.store == nil || ctx == nil || id == "" {
		return "", ErrInvalidOptions
	}
	session, err := providers.store.Load(ctx, id)
	if err != nil {
		return "", err
	}
	return session.Provider(), nil
}
