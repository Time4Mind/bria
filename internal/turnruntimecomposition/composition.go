// Package turnruntimecomposition assembles the controller-facing P4 turn path
// from explicit dependencies after durable state and safelog are open.
package turnruntimecomposition

import (
	"context"
	"errors"
	"fmt"

	"bria/internal/artifactruntimecomposition"
	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/observability"
	"bria/internal/observabilitycomposition"
	"bria/internal/p4runtimecomposition"
	"bria/internal/providerinputcomposition"
	"bria/internal/safelog"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/turnprocessing"
)

// Options name the bounded construction inputs. Paths, artifact policy, and
// the owner chat remain in Configuration and are never inferred here.
type Options struct {
	Configuration config.Config
	Telegram      *telegram.Client
	Settings      settings.Store
	Sessions      *storage.SessionStore
	Runtime       sessionruntime.Submitter
	Logger        *safelog.Logger
}

// Bundle is the controller-facing turn path. Its callback methods retain
// artifact retry routing with the same Finals processor that produced it.
type Bundle struct {
	Submitter     sessionruntime.Submitter
	InputPreparer turnprocessing.InputPreparer
	Attachments   turnprocessing.AttachmentCustody
	RuntimeEvents turnprocessing.RuntimeEventObserver
	Finals        turnprocessing.FinalProcessor
	artifacts     *artifactruntimecomposition.Bundle
}

// Open constructs artifact production before P4 and passes only the terminal
// final callback into P4. The final submitter retains Prepared input when it
// has that public capability, then receives non-blocking observability.
func Open(options Options) (*Bundle, error) {
	if options.Telegram == nil || options.Settings == nil || options.Sessions == nil || options.Runtime == nil {
		return nil, errors.New("turn runtime dependencies are required")
	}
	artifacts, _, err := artifactruntimecomposition.Open(artifactruntimecomposition.Options{Configuration: options.Configuration, Telegram: options.Telegram})
	if err != nil {
		return nil, fmt.Errorf("compose artifact runtime: %w", err)
	}
	bundle := &Bundle{Submitter: options.Runtime, artifacts: artifacts}
	if _, enabled := options.Configuration.P4Runtime(); enabled {
		structuredRuntime, ok := options.Runtime.(providerinputcomposition.Runtime)
		if !ok {
			return nil, errors.New("configured P4 provider runtime does not support structured input")
		}
		var finals turnprocessing.FinalProcessor
		if artifacts != nil {
			finals = artifacts.Finals
		}
		p4, _, err := p4runtimecomposition.Open(p4runtimecomposition.Options{
			Configuration: options.Configuration, Telegram: options.Telegram, Settings: options.Settings, Sessions: options.Sessions, Runtime: structuredRuntime, Finals: finals,
		})
		if err != nil {
			return nil, fmt.Errorf("compose P4 runtime: %w", err)
		}
		bundle.Submitter = p4.Submitter
		bundle.InputPreparer, bundle.Attachments = p4.InputPreparer, p4.Attachments
		bundle.RuntimeEvents, bundle.Finals = p4.RuntimeEvents, p4.Finals
	}
	bundle.Submitter = observeSubmitter(bundle.Submitter, options.Logger, options.Sessions)
	return bundle, nil
}

// WrapCallback reserves only artifact retry effects for the artifact bundle;
// every existing recovery effect continues to the supplied executor.
func (bundle *Bundle) WrapCallback(next telegramflow.CallbackExecutor) telegramflow.CallbackExecutor {
	if bundle == nil || bundle.artifacts == nil {
		return next
	}
	return bundle.artifacts.WrapCallback(next)
}

// BindPublisher completes the late artifact binding once telegramflow.New has
// produced its stable sender. Disabled P4 is a no-op with no side effects.
func (bundle *Bundle) BindPublisher(presenter *telegrambridge.Presenter, sender *telegramflow.Sender) error {
	if bundle == nil || bundle.artifacts == nil {
		return nil
	}
	return bundle.artifacts.BindPublisher(presenter, sender)
}

// sessionStoreProviderResolver reads the exact provider binding at the turn
// boundary. Resolver failure is absorbed by observabilitycomposition.
type sessionStoreProviderResolver struct{ store *storage.SessionStore }

func (resolver sessionStoreProviderResolver) ProviderForSession(ctx context.Context, id domain.SessionID) (domain.Provider, error) {
	if resolver.store == nil || ctx == nil || id == "" {
		return "", observabilitycomposition.ErrInvalidConfiguration
	}
	session, err := resolver.store.Load(ctx, id)
	if err != nil {
		return "", err
	}
	return session.Provider(), nil
}

// observeSubmitter never changes the provider result. It selects the prepared
// wrapper before the ordinary runtime wrapper so P4 attachment capability is
// retained through the controller boundary.
func observeSubmitter(submitter sessionruntime.Submitter, logger *safelog.Logger, sessions *storage.SessionStore) sessionruntime.Submitter {
	if submitter == nil {
		return nil
	}
	recorder, err := observability.New(logger)
	if err != nil {
		return submitter
	}
	providers := sessionStoreProviderResolver{store: sessions}
	if prepared, ok := submitter.(observabilitycomposition.PreparedRuntime); ok {
		wrapped, err := observabilitycomposition.NewPrepared(prepared, recorder, providers)
		if err == nil {
			return wrapped
		}
		return submitter
	}
	if runtime, ok := submitter.(observabilitycomposition.Runtime); ok {
		wrapped, err := observabilitycomposition.New(runtime, recorder, providers)
		if err == nil {
			return wrapped
		}
	}
	return submitter
}
