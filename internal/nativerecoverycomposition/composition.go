// Package nativerecoverycomposition binds native receipt readers to exact configured child environments.
package nativerecoverycomposition

import (
	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/recoveryruntime"
	"bria/internal/runtimefactory"
	"bria/internal/sessionruntime"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func Compose(configuration config.Config, commands *runtimefactory.CommandSet) (sessionruntime.AcceptedTurnReader, error) {
	var codex, claude sessionruntime.AcceptedTurnReader
	if configuration.ProviderEnabled(domain.ProviderCodex) {
		root := ""
		// Without a command snapshot only existing receipts can be inspected.
		// Normal runtime composition always supplies the exact child environment.
		if commands != nil {
			spec, ok := commands.CommandSpec(domain.ProviderCodex)
			if !ok {
				return nil, errors.New("Codex recovery command snapshot unavailable")
			}
			for _, value := range spec.Env {
				if strings.HasPrefix(value, "HOME=") {
					root = filepath.Join(strings.TrimPrefix(value, "HOME="), ".codex", "sessions")
				}
			}
			for _, value := range spec.Env {
				if strings.HasPrefix(value, "CODEX_HOME=") && value != "CODEX_HOME=" {
					root = filepath.Join(strings.TrimPrefix(value, "CODEX_HOME="), "sessions")
				}
			}
			if root == "" {
				return nil, errors.New("Codex recovery native history root unavailable")
			}
		}
		reader, err := recoveryruntime.NewNativeWithTranscriptRoot(configuration.StatePath+".native", domain.ProviderCodex, root)
		if err != nil {
			return nil, fmt.Errorf("compose Codex accepted-turn reader: %w", err)
		}
		codex = reader
	}
	if configuration.ProviderEnabled(domain.ProviderClaude) {
		reader, err := recoveryruntime.NewNative(configuration.StatePath+".native", domain.ProviderClaude)
		if err != nil {
			return nil, fmt.Errorf("compose Claude accepted-turn reader: %w", err)
		}
		claude = reader
	}
	if codex != nil && claude != nil {
		readers, err := recoveryruntime.NewProviderReaders(codex, claude)
		if err != nil {
			return nil, fmt.Errorf("compose provider accepted-turn readers: %w", err)
		}
		return readers, nil
	}
	if codex != nil {
		return codex, nil
	}
	return claude, nil
}
