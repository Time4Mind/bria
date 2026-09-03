// Package artifactruntimecomposition assembles the opt-in artifact final and
// manual retry path from explicit P4 configuration only.
package artifactruntimecomposition

import (
	"errors"
	"fmt"
	"time"

	"bria/internal/artifactcomposition"
	"bria/internal/artifactproduction"
	"bria/internal/artifactretrycomposition"
	"bria/internal/config"
	"bria/internal/secretfile"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/turnprocessing"
)

var ErrInvalidOptions = errors.New("artifact runtime composition is invalid")

// Options name the already-validated runtime dependencies. No path, secret,
// chat, or artifact policy is inferred outside Configuration.
type Options struct {
	Configuration config.Config
	Telegram      *telegram.Client
}

// Bundle exposes the controller-facing terminal callback while keeping retry
// callbacks and their Telegram publisher in this composition boundary.
type Bundle struct {
	Finals      turnprocessing.FinalProcessor
	retries     *artifactretrycomposition.Composition
	privateChat int64
}

// Open returns enabled=false without reading a secret or creating any path
// when runtime.p4 is absent. The retry-key buffer exists only inside
// secretfile.Use; artifactproduction copies the key it needs before that
// buffer is wiped.
func Open(options Options) (*Bundle, bool, error) {
	p4, enabled := options.Configuration.P4Runtime()
	if !enabled {
		return nil, false, nil
	}
	if options.Telegram == nil || options.Configuration.MediaLimits == nil || options.Configuration.PrivateChatID <= 0 {
		return nil, true, ErrInvalidOptions
	}
	var bundle *Bundle
	stage := "read retry key"
	err := secretfile.Use(p4.ArtifactRetryKey.SecretFile, secretfile.Options{
		MaxBytes: secretfile.HardMaxBytes, MinBytes: 32, TrimFinalNewline: true,
	}, func(key []byte) error {
		stage = "open artifact production"
		production, err := artifactproduction.Open(options.Telegram, artifactproduction.Config{
			ManifestDirectory: p4.ArtifactManifestDirectory,
			RetryDirectory:    p4.ArtifactRetryDirectory,
			AllowedRoots:      append([]string(nil), p4.ArtifactAllowedRoots...),
			MaxFileBytes:      options.Configuration.MediaLimits.UploadBytes,
			ChatID:            telegram.ChatID(options.Configuration.PrivateChatID),
			RetryKey:          key,
			RetryTTL:          time.Duration(p4.ArtifactRetryTTLSeconds) * time.Second,
		})
		if err != nil {
			return err
		}
		stage = "open artifact delivery"
		delivery, err := artifactcomposition.Open(production)
		if err != nil {
			return err
		}
		stage = "open artifact retry"
		retries, err := artifactretrycomposition.Open(bindingStorePath(p4.ArtifactRetryDirectory), delivery, nil)
		if err != nil {
			return err
		}
		bundle = &Bundle{Finals: retries, retries: retries, privateChat: options.Configuration.PrivateChatID}
		return nil
	})
	if err != nil {
		return nil, true, fmt.Errorf("%w: %s", ErrInvalidOptions, stage)
	}
	if bundle == nil {
		return nil, true, ErrInvalidOptions
	}
	return bundle, true, nil
}

// WrapCallback gives artifact retry callbacks their single explicit route and
// passes every established recovery callback untouched to next.
func (bundle *Bundle) WrapCallback(next telegramflow.CallbackExecutor) telegramflow.CallbackExecutor {
	if bundle == nil || bundle.retries == nil {
		return next
	}
	return artifactretrycomposition.WrapCallback(bundle.retries, next)
}

// BindPublisher is intentionally late: telegramflow.New owns the stable
// sender. artifactretrycomposition enforces a one-shot publisher binding.
func (bundle *Bundle) BindPublisher(presenter *telegrambridge.Presenter, sender *telegramflow.Sender) error {
	if bundle == nil || bundle.retries == nil {
		return ErrInvalidOptions
	}
	publisher, err := artifactretrycomposition.NewTelegramPublisher(bundle.privateChat, presenter, sender)
	if err != nil {
		return ErrInvalidOptions
	}
	if err := bundle.retries.BindPublisher(publisher); err != nil {
		return ErrInvalidOptions
	}
	return nil
}

func bindingStorePath(retryDirectory string) string { return retryDirectory + ".bindings.json" }
