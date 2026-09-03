package updateruntime

import (
	"crypto/ed25519"
	"errors"
	"path/filepath"

	"bria/internal/update"
	"bria/internal/updateflow"
	"bria/internal/updateinstall"
)

var ErrInvalidRuntime = errors.New("invalid packaged update runtime")

// RuntimeConfig is deliberately explicit: command composition chooses the
// release source, trust root, storage paths, packaged commands and physical
// health/state readers. updateruntime does not choose a schedule or endpoint.
type RuntimeConfig struct {
	Source            SourceConfig
	StageDirectory    string
	StateDirectory    string
	MaximumStageBytes int64
	TrustedKeys       update.TrustedKeys
	Commands          updateinstall.CommandFactory
	Runner            updateinstall.CommandRunner
	State             updateinstall.InstallStateReader
	Health            updateinstall.HealthReader
	InstallLock       updateinstall.InstallLocker
	OperationProof    updateinstall.OperationProofVerifier
	Integrity         updateinstall.CurrentIntegrityVerifier
}

type Runtime struct {
	Service updateflow.Service
}

func OpenRuntime(config RuntimeConfig) (*Runtime, error) {
	if !filepath.IsAbs(config.StageDirectory) || !filepath.IsAbs(config.StateDirectory) ||
		config.MaximumStageBytes <= 0 || config.Commands == nil || config.Runner == nil ||
		config.State == nil || config.Health == nil || config.InstallLock == nil || config.OperationProof == nil ||
		config.Integrity == nil || len(config.TrustedKeys) == 0 {
		return nil, ErrInvalidRuntime
	}
	trustedKeys := make(update.TrustedKeys, len(config.TrustedKeys))
	for keyID, publicKey := range config.TrustedKeys {
		if invalidText(keyID, 128) || len(publicKey) != ed25519.PublicKeySize {
			return nil, ErrInvalidRuntime
		}
		trustedKeys[keyID] = append(ed25519.PublicKey(nil), publicKey...)
	}
	source, err := NewReleaseSource(config.Source)
	if err != nil {
		return nil, err
	}
	stager, err := OpenLocalStager(config.StageDirectory, config.MaximumStageBytes)
	if err != nil {
		return nil, err
	}
	store, err := updateflow.OpenFileStore(config.StateDirectory)
	if err != nil {
		return nil, err
	}
	installer := updateinstall.PackagedInstaller{
		Commands: config.Commands, Runner: config.Runner, State: config.State, Lock: config.InstallLock, Proof: config.OperationProof,
	}
	postflight := updateinstall.VersionedPostflight{Health: config.Health, State: config.State, Integrity: config.Integrity}
	return &Runtime{Service: updateflow.Service{
		Source: source, Stager: stager, Installer: installer, Postflight: postflight, Store: store, TrustedKeys: trustedKeys,
	}}, nil
}
