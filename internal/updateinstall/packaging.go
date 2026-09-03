package updateinstall

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bria/internal/update"
	"bria/internal/updateflow"
)

type PackagedCommandConfig struct {
	Platform           string
	Arch               string
	InstallExecutable  string
	RollbackExecutable string
	VerifierExecutable string
	TrustFile          string
	InstallRoot        string
	ConfigFile         string
}

// PackagedCommandFactory binds the stable operation identity and the expected
// preserved-state fingerprint into the fail-closed packaging entrypoints.
type PackagedCommandFactory struct{ Config PackagedCommandConfig }

func (f PackagedCommandFactory) InstallCommand(request updateflow.InstallRequest) (Command, error) {
	if !validPackagedCommandConfig(f.Config) || invalidInstallRequest(request) ||
		!validScriptLabel(request.OperationID) || !validScriptLabel(request.PriorState) ||
		request.Stage.Artifact.Arch != f.Config.Arch || request.Stage.Artifact.Platform != artifactPlatform(f.Config.Platform) {
		return Command{}, ErrInvalidInstaller
	}
	return Command{Executable: f.Config.InstallExecutable, Arguments: []string{
		f.Config.Platform, f.Config.Arch, request.TargetVersion, request.Stage.Reference, f.Config.TrustFile,
		f.Config.InstallRoot, f.Config.ConfigFile, f.Config.VerifierExecutable, request.OperationID, request.NodeID,
		request.FromVersion, request.PriorState,
	}}, nil
}

func (f PackagedCommandFactory) RollbackCommand(request updateflow.RollbackRequest) (Command, error) {
	if !validPackagedCommandConfig(f.Config) || invalidRollbackRequest(request) ||
		!validScriptLabel(request.OperationID) || !validScriptLabel(request.TargetState) {
		return Command{}, ErrInvalidInstaller
	}
	return Command{Executable: f.Config.RollbackExecutable, Arguments: []string{
		f.Config.InstallRoot, f.Config.ConfigFile, f.Config.TrustFile, f.Config.VerifierExecutable,
		request.OperationID, request.NodeID, request.FromVersion, request.TargetVersion, request.TargetState,
	}}, nil
}

func validScriptLabel(value string) bool {
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._:+-", character) {
			continue
		}
		return false
	}
	return value != "" && !strings.HasPrefix(value, ".") && !strings.Contains(value, "..")
}

func validPackagedCommandConfig(config PackagedCommandConfig) bool {
	if config.Platform != "macos" && config.Platform != "linux" && config.Platform != "wsl" {
		return false
	}
	if config.Arch != "amd64" && config.Arch != "arm64" {
		return false
	}
	for _, path := range []string{
		config.InstallExecutable, config.RollbackExecutable, config.VerifierExecutable,
		config.TrustFile, config.InstallRoot, config.ConfigFile,
	} {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			return false
		}
	}
	return true
}

func artifactPlatform(platform string) string {
	if platform == "macos" {
		return "darwin"
	}
	return "linux"
}

type FileInstallStateReader struct {
	installRoot     string
	fingerprintPath string
}

func OpenFileInstallStateReader(installRoot, fingerprintPath string) (*FileInstallStateReader, error) {
	if !filepath.IsAbs(installRoot) || !filepath.IsAbs(fingerprintPath) {
		return nil, ErrInvalidInstaller
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(installRoot))
	if err != nil {
		return nil, ErrInvalidInstaller
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || !privateInstallDirectory(root) {
		return nil, ErrInvalidInstaller
	}
	return &FileInstallStateReader{installRoot: root, fingerprintPath: filepath.Clean(fingerprintPath)}, nil
}

func (r *FileInstallStateReader) ReadInstalledState(ctx context.Context, nodeID string) (InstalledState, error) {
	if r == nil || r.installRoot == "" || invalidText(nodeID, 1024) {
		return InstalledState{}, ErrInvalidInstaller
	}
	if err := ctx.Err(); err != nil {
		return InstalledState{}, err
	}
	current := filepath.Join(r.installRoot, "current")
	info, err := os.Lstat(current)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return InstalledState{}, ErrInstallerState
	}
	target, err := os.Readlink(current)
	if err != nil || !strings.HasPrefix(target, "releases/") {
		return InstalledState{}, ErrInstallerState
	}
	version := strings.TrimPrefix(target, "releases/")
	if invalidText(version, 128) || strings.ContainsAny(version, `/\\`) {
		return InstalledState{}, ErrInstallerState
	}
	releaseInfo, err := os.Lstat(filepath.Join(r.installRoot, target))
	if err != nil || !releaseInfo.IsDir() || releaseInfo.Mode()&os.ModeSymlink != 0 {
		return InstalledState{}, ErrInstallerState
	}
	fingerprint, err := readExactFingerprint(r.fingerprintPath)
	if err != nil {
		return InstalledState{}, err
	}
	return InstalledState{Version: version, StateFingerprint: fingerprint}, nil
}

func readExactFingerprint(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > 4097 {
		return "", ErrInstallerState
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ErrInstallerState
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return "", ErrInstallerState
	}
	content, err := io.ReadAll(io.LimitReader(file, 4098))
	if err != nil || len(content) > 4097 {
		return "", ErrInstallerState
	}
	value := strings.TrimSuffix(string(content), "\n")
	if invalidText(value, 4096) || strings.ContainsAny(value, "\r\n") {
		return "", ErrInstallerState
	}
	return value, nil
}

type PackagedHealthConfig struct {
	Platform    string
	Executable  string
	InstallRoot string
	ConfigFile  string
	ServiceFile string
	Telegram    bool
}

type ReadinessObserver interface {
	ObserveHealth(context.Context, string, string) (update.HealthReceipt, error)
}

// PackagedHealthReader first requires the packaged local postflight to pass,
// then returns independently observed provider/session/coordinator readiness.
type PackagedHealthReader struct {
	Config   PackagedHealthConfig
	Runner   CommandRunner
	Observer ReadinessObserver
}

type FileOperationProofVerifier struct {
	Platform           string
	Arch               string
	InstallRoot        string
	TrustFile          string
	VerifierExecutable string
	Runner             CommandRunner
}

func (v FileOperationProofVerifier) VerifyOperation(ctx context.Context, proof OperationProof) error {
	if !v.valid() || !validOperationProof(proof) {
		return ErrInstallerProof
	}
	receipt := filepath.Join(v.InstallRoot, "update-operation-receipts", proof.OperationID)
	want := []byte(proof.OperationID + "\n" + proof.NodeID + "\n" + proof.FromVersion + "\n" +
		proof.TargetVersion + "\n" + proof.State + "\n")
	content, err := readPrivateRegular(receipt, 16<<10)
	if err == nil {
		if !bytes.Equal(content, want) {
			return ErrInstallerProof
		}
		return v.verifyCurrent(ctx, proof.TargetVersion)
	}
	if _, statErr := os.Lstat(receipt); !errors.Is(statErr, os.ErrNotExist) {
		return ErrInstallerProof
	}
	pending := filepath.Join(filepath.Dir(receipt), ".pending-"+proof.OperationID)
	pendingContent, err := readPrivateRegular(pending, 16<<10)
	if err != nil || !bytes.Equal(pendingContent, want) {
		return ErrInstallerProof
	}
	if err := v.verifyCurrent(ctx, proof.TargetVersion); err != nil {
		return err
	}
	if err := os.Rename(pending, receipt); err != nil {
		return ErrInstallerProof
	}
	if err := syncInstallDirectory(filepath.Dir(receipt)); err != nil {
		return ErrInstallerProof
	}
	confirmed, err := readPrivateRegular(receipt, 16<<10)
	if err != nil || !bytes.Equal(confirmed, want) {
		return ErrInstallerProof
	}
	return nil
}

func (v FileOperationProofVerifier) VerifyCurrent(ctx context.Context, _ string, version string) error {
	if !v.valid() || invalidText(version, 128) {
		return ErrInstallerProof
	}
	return v.verifyCurrent(ctx, version)
}

func (v FileOperationProofVerifier) verifyCurrent(ctx context.Context, version string) error {
	current := filepath.Join(v.InstallRoot, "current")
	target, err := os.Readlink(current)
	if err != nil || target != "releases/"+version {
		return ErrInstallerProof
	}
	installed := filepath.Join(v.InstallRoot, "releases", version)
	receiptDirectory := filepath.Join(v.InstallRoot, "release-receipts", version)
	artifactOS := artifactPlatform(v.Platform)
	archive := filepath.Join(receiptDirectory, "bria_"+version+"_"+artifactOS+"_"+v.Arch+".tar.gz")
	command := Command{Executable: v.VerifierExecutable, Arguments: []string{
		"verify-installed", "-manifest", filepath.Join(receiptDirectory, "release-manifest.json"),
		"-artifact", archive, "-installed", installed, "-version", version,
		"-platform", artifactOS, "-arch", v.Arch, "-trust-file", v.TrustFile,
	}}
	if err := v.Runner.Run(ctx, command); err != nil {
		return ErrInstallerProof
	}
	return nil
}

func (v FileOperationProofVerifier) valid() bool {
	if v.Runner == nil || !validPackagedCommandConfig(PackagedCommandConfig{
		Platform: v.Platform, Arch: v.Arch, InstallExecutable: v.VerifierExecutable,
		RollbackExecutable: v.VerifierExecutable, VerifierExecutable: v.VerifierExecutable,
		TrustFile: v.TrustFile, InstallRoot: v.InstallRoot, ConfigFile: v.TrustFile,
	}) {
		return false
	}
	if !privateInstallDirectory(v.InstallRoot) {
		return false
	}
	relative, err := filepath.Rel(v.InstallRoot, v.VerifierExecutable)
	return err == nil && (relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func validOperationProof(proof OperationProof) bool {
	return (proof.Kind == "install" || proof.Kind == "rollback") && validScriptLabel(proof.OperationID) &&
		validScriptLabel(proof.NodeID) && validScriptLabel(proof.FromVersion) && validScriptLabel(proof.TargetVersion) &&
		validScriptLabel(proof.State)
}

func readPrivateRegular(path string, maximum int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > maximum || before.Mode().Perm() != 0o600 {
		return nil, ErrInstallerProof
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInstallerProof
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, ErrInstallerProof
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) > maximum {
		return nil, ErrInstallerProof
	}
	return content, nil
}

func (r PackagedHealthReader) ReadHealth(ctx context.Context, nodeID, version string) (update.HealthReceipt, error) {
	if !validPackagedHealthConfig(r.Config) || r.Runner == nil || r.Observer == nil ||
		invalidText(nodeID, 1024) || invalidText(version, 128) {
		return update.HealthReceipt{}, ErrInvalidPostflight
	}
	arguments := []string{r.Config.Platform, r.Config.InstallRoot, version, r.Config.ConfigFile, r.Config.ServiceFile}
	if r.Config.Telegram {
		arguments = append(arguments, "--telegram")
	}
	if err := r.Runner.Run(ctx, Command{Executable: r.Config.Executable, Arguments: arguments}); err != nil {
		return update.HealthReceipt{}, ErrPostflightMismatch
	}
	receipt, err := r.Observer.ObserveHealth(ctx, nodeID, version)
	if err != nil {
		return update.HealthReceipt{}, err
	}
	if receipt.NodeID != nodeID || receipt.RunningVersion != version {
		return update.HealthReceipt{}, ErrPostflightMismatch
	}
	return receipt, nil
}

func validPackagedHealthConfig(config PackagedHealthConfig) bool {
	if config.Platform != "macos" && config.Platform != "linux" && config.Platform != "wsl" {
		return false
	}
	for _, path := range []string{config.Executable, config.InstallRoot, config.ConfigFile, config.ServiceFile} {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			return false
		}
	}
	return true
}
