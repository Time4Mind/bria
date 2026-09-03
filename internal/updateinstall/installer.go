package updateinstall

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"bria/internal/updateflow"
)

var (
	ErrInvalidInstaller = errors.New("invalid packaged update installer")
	ErrInstallerState   = errors.New("packaged update installer state mismatch")
	ErrInstallerCommand = errors.New("packaged update command failed")
	ErrInstallerProof   = errors.New("packaged update operation proof mismatch")
)

type Command struct {
	Executable string
	Arguments  []string
}

type CommandFactory interface {
	InstallCommand(updateflow.InstallRequest) (Command, error)
	RollbackCommand(updateflow.RollbackRequest) (Command, error)
}

type CommandRunner interface {
	Run(context.Context, Command) error
}

type InstalledState struct {
	Version          string
	StateFingerprint string
}

type InstallStateReader interface {
	ReadInstalledState(context.Context, string) (InstalledState, error)
}

type OperationProof struct {
	Kind          string
	OperationID   string
	NodeID        string
	FromVersion   string
	TargetVersion string
	State         string
}

type OperationProofVerifier interface {
	VerifyOperation(context.Context, OperationProof) error
}

type PackagedInstaller struct {
	Commands CommandFactory
	Runner   CommandRunner
	State    InstallStateReader
	Lock     InstallLocker
	Proof    OperationProofVerifier
}

func (i PackagedInstaller) Install(ctx context.Context, request updateflow.InstallRequest) (updateflow.InstallReceipt, error) {
	if !i.valid() || invalidInstallRequest(request) {
		return updateflow.InstallReceipt{}, ErrInvalidInstaller
	}
	var receipt updateflow.InstallReceipt
	err := i.Lock.WithLock(ctx, func() error {
		var installErr error
		receipt, installErr = i.installLocked(ctx, request)
		return installErr
	})
	return receipt, err
}

func (i PackagedInstaller) installLocked(ctx context.Context, request updateflow.InstallRequest) (updateflow.InstallReceipt, error) {
	before, err := i.State.ReadInstalledState(ctx, request.NodeID)
	if err != nil {
		return updateflow.InstallReceipt{}, err
	}
	if before.Version == request.TargetVersion && before.StateFingerprint == request.PriorState {
		if err := i.Proof.VerifyOperation(ctx, installProof(request)); err != nil {
			return updateflow.InstallReceipt{}, ErrInstallerProof
		}
		return installReceipt(request.OperationID, request.NodeID, request.TargetVersion, request.PriorState), nil
	}
	if before.Version != request.FromVersion || before.StateFingerprint != request.PriorState {
		return updateflow.InstallReceipt{}, ErrInstallerState
	}
	command, err := i.Commands.InstallCommand(request)
	if err != nil || invalidCommand(command) {
		return updateflow.InstallReceipt{}, ErrInvalidInstaller
	}
	runErr := i.Runner.Run(ctx, command)
	after, err := i.State.ReadInstalledState(ctx, request.NodeID)
	if err != nil {
		return updateflow.InstallReceipt{}, err
	}
	if after.Version == request.TargetVersion && after.StateFingerprint == request.PriorState {
		if err := i.Proof.VerifyOperation(ctx, installProof(request)); err != nil {
			return updateflow.InstallReceipt{}, ErrInstallerProof
		}
		return installReceipt(request.OperationID, request.NodeID, after.Version, after.StateFingerprint), nil
	}
	if runErr != nil {
		return updateflow.InstallReceipt{}, ErrInstallerCommand
	}
	return updateflow.InstallReceipt{}, ErrInstallerState
}

func (i PackagedInstaller) Rollback(ctx context.Context, request updateflow.RollbackRequest) (updateflow.InstallReceipt, error) {
	if !i.valid() || invalidRollbackRequest(request) {
		return updateflow.InstallReceipt{}, ErrInvalidInstaller
	}
	var receipt updateflow.InstallReceipt
	err := i.Lock.WithLock(ctx, func() error {
		var rollbackErr error
		receipt, rollbackErr = i.rollbackLocked(ctx, request)
		return rollbackErr
	})
	return receipt, err
}

func (i PackagedInstaller) rollbackLocked(ctx context.Context, request updateflow.RollbackRequest) (updateflow.InstallReceipt, error) {
	before, err := i.State.ReadInstalledState(ctx, request.NodeID)
	if err != nil {
		return updateflow.InstallReceipt{}, err
	}
	if before.Version == request.TargetVersion && before.StateFingerprint == request.TargetState {
		if err := i.Proof.VerifyOperation(ctx, rollbackProof(request)); err == nil {
			return installReceipt(request.OperationID, request.NodeID, request.TargetVersion, request.TargetState), nil
		}
		command, err := i.Commands.RollbackCommand(request)
		if err != nil || invalidCommand(command) {
			return updateflow.InstallReceipt{}, ErrInvalidInstaller
		}
		runErr := i.Runner.Run(ctx, command)
		after, err := i.State.ReadInstalledState(ctx, request.NodeID)
		if err != nil {
			return updateflow.InstallReceipt{}, err
		}
		if after.Version == request.TargetVersion && after.StateFingerprint == request.TargetState {
			if err := i.Proof.VerifyOperation(ctx, rollbackProof(request)); err != nil {
				return updateflow.InstallReceipt{}, ErrInstallerProof
			}
			return installReceipt(request.OperationID, request.NodeID, after.Version, after.StateFingerprint), nil
		}
		if runErr != nil {
			return updateflow.InstallReceipt{}, ErrInstallerCommand
		}
		return updateflow.InstallReceipt{}, ErrInstallerState
	}
	if before.Version != request.FromVersion {
		return updateflow.InstallReceipt{}, ErrInstallerState
	}
	command, err := i.Commands.RollbackCommand(request)
	if err != nil || invalidCommand(command) {
		return updateflow.InstallReceipt{}, ErrInvalidInstaller
	}
	runErr := i.Runner.Run(ctx, command)
	after, err := i.State.ReadInstalledState(ctx, request.NodeID)
	if err != nil {
		return updateflow.InstallReceipt{}, err
	}
	if after.Version == request.TargetVersion && after.StateFingerprint == request.TargetState {
		if err := i.Proof.VerifyOperation(ctx, rollbackProof(request)); err != nil {
			return updateflow.InstallReceipt{}, ErrInstallerProof
		}
		return installReceipt(request.OperationID, request.NodeID, after.Version, after.StateFingerprint), nil
	}
	if runErr != nil {
		return updateflow.InstallReceipt{}, ErrInstallerCommand
	}
	return updateflow.InstallReceipt{}, ErrInstallerState
}

func (i PackagedInstaller) valid() bool {
	return i.Commands != nil && i.Runner != nil && i.State != nil && i.Lock != nil && i.Proof != nil
}

func installProof(request updateflow.InstallRequest) OperationProof {
	return OperationProof{Kind: "install", OperationID: request.OperationID, NodeID: request.NodeID,
		FromVersion: request.FromVersion, TargetVersion: request.TargetVersion, State: request.PriorState}
}

func rollbackProof(request updateflow.RollbackRequest) OperationProof {
	return OperationProof{Kind: "rollback", OperationID: request.OperationID, NodeID: request.NodeID,
		FromVersion: request.FromVersion, TargetVersion: request.TargetVersion, State: request.TargetState}
}

func invalidInstallRequest(request updateflow.InstallRequest) bool {
	return invalidText(request.OperationID, 256) || invalidText(request.NodeID, 1024) ||
		invalidText(request.FromVersion, 128) || invalidText(request.TargetVersion, 128) ||
		invalidText(request.PriorState, 4096) || invalidStageReceipt(request.Stage, request.NodeID, request.TargetVersion)
}

func invalidStageReceipt(receipt updateflow.StageReceipt, nodeID, version string) bool {
	digest, err := hex.DecodeString(receipt.Artifact.SHA256)
	return invalidText(receipt.OperationID, 256) || receipt.NodeID != nodeID || receipt.Version != version ||
		invalidText(receipt.Reference, 4096) || !filepath.IsAbs(receipt.Reference) ||
		receipt.Artifact.Name == "" || receipt.Artifact.Name != filepath.Base(receipt.Artifact.Name) ||
		invalidText(receipt.Artifact.Platform, 64) || invalidText(receipt.Artifact.Arch, 64) || receipt.Artifact.Size < 0 ||
		err != nil || len(digest) != 32 || strings.ToLower(receipt.Artifact.SHA256) != receipt.Artifact.SHA256
}

func invalidRollbackRequest(request updateflow.RollbackRequest) bool {
	return invalidText(request.OperationID, 256) || invalidText(request.NodeID, 1024) ||
		invalidText(request.FromVersion, 128) || invalidText(request.TargetVersion, 128) || invalidText(request.TargetState, 4096)
}

func invalidCommand(command Command) bool {
	if !filepath.IsAbs(command.Executable) || strings.ContainsRune(command.Executable, 0) {
		return true
	}
	for _, argument := range command.Arguments {
		if strings.ContainsRune(argument, 0) {
			return true
		}
	}
	return false
}

func installReceipt(operationID, nodeID, version, state string) updateflow.InstallReceipt {
	return updateflow.InstallReceipt{
		OperationID: operationID, NodeID: nodeID, RunningVersion: version, State: state, Applied: true,
	}
}

// DirectRunner invokes the configured packaged executable directly. It never
// passes the command through a shell and never includes output in returned
// errors.
type DirectRunner struct{}

func (DirectRunner) Run(ctx context.Context, command Command) error {
	if invalidCommand(command) {
		return ErrInvalidInstaller
	}
	info, err := os.Lstat(command.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return ErrInvalidInstaller
	}
	process := exec.CommandContext(ctx, command.Executable, command.Arguments...)
	process.Stdin = nil
	process.Stdout = nil
	process.Stderr = nil
	if err := process.Run(); err != nil {
		return ErrInstallerCommand
	}
	return nil
}
