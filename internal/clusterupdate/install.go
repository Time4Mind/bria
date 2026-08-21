package clusterupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/binaryidentity"
)

const maxExtractedReleaseBytes = 1 << 30

func extractRelease(archivePath, destination string) error {
	stage := destination + ".stage"
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.Mkdir(stage, 0o700); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(stage)
		}
	}()
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return errors.New("update artifact is not gzip")
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	var extracted int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("update tar is malformed")
		}
		name := filepath.Clean(strings.TrimPrefix(header.Name, "./"))
		if name == "." && header.Typeflag == tar.TypeDir {
			continue
		}
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return errors.New("update tar contains an unsafe path")
		}
		target := filepath.Join(stage, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			extracted += header.Size
			if header.Size < 0 || extracted > maxExtractedReleaseBytes {
				return errors.New("update tar expands beyond limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(output, reader, header.Size)
			closeErr := output.Close()
			if copyErr != nil || closeErr != nil {
				return errors.New("extract update file")
			}
			if header.FileInfo().Mode()&0o111 != 0 {
				if err := os.Chmod(target, 0o755); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("update tar contains unsupported entry %q", name)
		}
	}
	// The control process owns the staged release, but an isolated backend
	// runner normally has a different uid.  Keep the release immutable while
	// allowing that runner to traverse the directory and execute the binary.
	// Without this, the node can restart into the new release while the runner
	// remains on the old executable and then fails with EACCES on its next
	// restart.
	if err := os.Chmod(stage, 0o755); err != nil {
		return err
	}
	if err := os.Rename(stage, destination); err != nil {
		return err
	}
	success = true
	return nil
}

type installedReleaseMetadata struct {
	Schema         int    `json:"schema"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	BuiltAt        string `json:"built_at"`
	BinarySHA256   string `json:"binary_sha256"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	NodeProtocol   int    `json:"node_protocol"`
}

func verifyReleaseBinary(
	destination, version string,
	minimumNodeProtocol int,
	artifactSHA256 string,
) (string, error) {
	binary := releaseBinary(destination)
	output, err := exec.Command(binary, "version").Output()
	if err != nil {
		return "", fmt.Errorf("verify staged Bria binary: %w", err)
	}
	var value struct {
		Version      string `json:"version"`
		Commit       string `json:"commit"`
		BuiltAt      string `json:"built_at"`
		BinarySHA256 string `json:"binary_sha256"`
		NodeProtocol int    `json:"node_protocol"`
	}
	if json.Unmarshal(output, &value) != nil || value.Version != version {
		return "", errors.New("staged Bria version does not match manifest")
	}
	if minimumNodeProtocol > 0 && value.NodeProtocol < minimumNodeProtocol {
		return "", errors.New("staged Bria binary does not satisfy manifest node protocol")
	}
	if !exactReleaseCommit(value.Commit) || !exactReleaseTimestamp(value.BuiltAt) {
		return "", errors.New("staged Bria binary provenance is incomplete")
	}
	actual, err := binaryidentity.SHA256(binary)
	if err != nil || value.BinarySHA256 == "" || value.BinarySHA256 != actual {
		return "", errors.New("staged Bria binary identity does not match executable")
	}
	metadata := installedReleaseMetadata{
		Schema: 1, Version: value.Version, Commit: value.Commit, BuiltAt: value.BuiltAt,
		BinarySHA256: actual, ArtifactSHA256: artifactSHA256, NodeProtocol: value.NodeProtocol,
	}
	if err := normalizeRuntimeRelease(destination); err != nil {
		return "", err
	}
	if err := writeInstalledReleaseMetadata(destination, metadata); err != nil {
		return "", err
	}
	if err := freezeInstalledRelease(destination); err != nil {
		return "", err
	}
	return actual, nil
}

func normalizeRuntimeRelease(destination string) error {
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "bria", "bria.exe", "bria-apple-speech", "release.json":
			continue
		}
		if err := os.RemoveAll(filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func exactReleaseCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' && char < 'a' || char > 'f' {
			return false
		}
	}
	return true
}

func exactReleaseTimestamp(value string) bool {
	if value == "" || len(value) > 20 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func freezeInstalledRelease(destination string) error {
	return filepath.WalkDir(destination, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("installed release contains a symlink")
		}
		if entry.IsDir() {
			// Keep directories traversable and removable by bounded retention.
			// Release files themselves are read-only and are never overwritten.
			return os.Chmod(path, 0o755)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("installed release contains a non-regular file")
		}
		mode := os.FileMode(0o444)
		if info.Mode()&0o111 != 0 {
			mode = 0o555
		}
		return os.Chmod(path, mode)
	})
}

func writeInstalledReleaseMetadata(destination string, metadata installedReleaseMetadata) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(destination, "release.json")
	temporary, err := os.CreateTemp(destination, ".release-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o444); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func verifyInstalledReleaseMetadata(destination string) error {
	data, err := os.ReadFile(filepath.Join(destination, "release.json"))
	if err != nil || len(data) > 8<<10 {
		return errors.New("existing release provenance is unavailable")
	}
	var metadata installedReleaseMetadata
	if json.Unmarshal(data, &metadata) != nil || metadata.Schema != 1 ||
		metadata.Version == "" || !exactReleaseCommit(metadata.Commit) ||
		!exactReleaseTimestamp(metadata.BuiltAt) {
		return errors.New("existing release provenance is invalid")
	}
	actual, err := binaryidentity.SHA256(releaseBinary(destination))
	if err != nil || metadata.BinarySHA256 != actual {
		return errors.New("existing release binary does not match provenance")
	}
	return nil
}

func releaseBinary(destination string) string {
	binary := filepath.Join(destination, "bria")
	if _, err := os.Stat(binary); err != nil {
		binary += ".exe"
	}
	return binary
}

func (m *Manager) switchCurrent(request Request, destination string) error {
	lock, err := acquireInstallLock(m.config.InstallRoot, time.Now())
	if err != nil {
		return err
	}
	defer lock.Close()
	current := m.config.ActivationPath
	destinationTarget := filepath.Join(destination, "bria")
	if strings.EqualFold(filepath.Ext(current), ".exe") {
		destinationTarget += ".exe"
	}
	directoryLink := filepath.Base(filepath.Dir(current)) == "current"
	if directoryLink {
		current = filepath.Dir(current)
		destinationTarget = destination
	}
	previous := ""
	info, inspectErr := os.Lstat(current)
	if inspectErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(current)
		if err != nil {
			return fmt.Errorf("resolve current release: %w", err)
		}
		previous = target
	} else if inspectErr == nil && info.IsDir() {
		return errors.New("legacy current directory must be migrated by the platform installer")
	} else if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
		return fmt.Errorf("inspect current release: %w", inspectErr)
	}
	if previous == "" && !directoryLink {
		if info, err := os.Lstat(current); err == nil && info.Mode().IsRegular() {
			previousDir := filepath.Join(m.config.InstallRoot, "releases", "pre-update-"+safeReleaseName(request.UpdateID))
			if err := os.MkdirAll(previousDir, 0o700); err != nil {
				return err
			}
			previous = filepath.Join(previousDir, filepath.Base(current))
			if err := copyRegularFile(current, previous, info.Mode().Perm()); err != nil {
				return fmt.Errorf("retain current executable: %w", err)
			}
		}
	}
	if previous == "" {
		if executable, executableErr := os.Executable(); executableErr == nil {
			previous = executable
			if directoryLink {
				previous = filepath.Dir(executable)
			}
		}
	}
	if previous == "" || !filepath.IsAbs(previous) {
		return errors.New("current Bria release has no rollback target")
	}
	previousBinary := previous
	if directoryLink {
		previousBinary = filepath.Join(previous, filepath.Base(destinationTarget))
	}
	previousSHA256, err := binaryidentity.SHA256(previousBinary)
	if err != nil {
		return errors.New("current Bria rollback binary is invalid")
	}
	nextBinary := destinationTarget
	if directoryLink {
		nextBinary = filepath.Join(destinationTarget, filepath.Base(previousBinary))
	}
	nextSHA256, err := binaryidentity.SHA256(nextBinary)
	if err != nil {
		return errors.New("next Bria binary is invalid")
	}
	releasesRoot := filepath.Join(m.config.InstallRoot, "releases")
	if !pendingReleaseTarget(releasesRoot, previous) || !pendingReleaseTarget(releasesRoot, destinationTarget) {
		return errors.New("update activation target is outside the immutable release root")
	}
	pendingPath := filepath.Join(m.config.InstallRoot, "update-pending.json")
	if err := writePending(pendingPath, pendingUpdate{
		NodeID: request.NodeID, UpdateID: request.UpdateID, Version: request.Version,
		Previous: previous, PreviousSHA256: previousSHA256,
		Next: destinationTarget, NextSHA256: nextSHA256, CurrentLink: current,
	}); err != nil {
		return err
	}
	if m.config.Watchdog != nil {
		err = m.config.Watchdog(request)
	} else {
		err = m.startWatchdog(request)
	}
	if err != nil {
		_ = os.Remove(pendingPath)
		return err
	}
	previousLink := filepath.Join(m.config.InstallRoot, "previous")
	priorPrevious, priorPreviousOK, err := symlinkTarget(previousLink)
	if err != nil {
		_ = os.Remove(pendingPath)
		return err
	}
	if err := replaceSymlink(previousLink, previous); err != nil {
		_ = os.Remove(pendingPath)
		return err
	}
	if err := replaceSymlink(current, destinationTarget); err != nil {
		if priorPreviousOK {
			_ = replaceSymlink(previousLink, priorPrevious)
		} else {
			_ = os.Remove(previousLink)
		}
		_ = os.Remove(pendingPath)
		return err
	}
	return nil
}

func symlinkTarget(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", false, errors.New("activation pointer is not a symlink")
	}
	target, err := os.Readlink(path)
	return target, err == nil, err
}

func replaceSymlink(path, target string) error {
	if !filepath.IsAbs(path) || !filepath.IsAbs(target) {
		return errors.New("activation symlink paths must be absolute")
	}
	temporary := path + ".new"
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > binaryidentity.MaxExecutableBytes {
		return errors.New("current executable is not a bounded regular file")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode&0o777)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, binaryidentity.MaxExecutableBytes+1))
	closeErr := output.Close()
	if copyErr != nil || written != info.Size() || written > binaryidentity.MaxExecutableBytes {
		_ = os.Remove(destination)
		if copyErr != nil {
			return copyErr
		}
		return errors.New("current executable copy is incomplete")
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func Watchdog(ctx context.Context, installRoot, updateID string, pid int, timeout time.Duration) error {
	if !filepath.IsAbs(installRoot) || strings.TrimSpace(updateID) == "" || pid <= 1 || timeout <= 0 {
		return errors.New("invalid update watchdog request")
	}
	pendingPath := filepath.Join(filepath.Clean(installRoot), "update-pending.json")
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(pendingPath); errors.Is(err, os.ErrNotExist) {
				return nil
			}
		case <-timer.C:
			return rollbackPending(pendingPath, updateID, pid)
		}
	}
}

func rollbackPending(path, updateID string, pid int) error {
	pending, err := restorePending(path, updateID)
	if err != nil {
		return err
	}
	status := Status{
		NodeID: pending.NodeID, UpdateID: pending.UpdateID, Version: pending.Version,
		Phase: PhaseFailed, Error: "new version did not become ready; rolled back",
	}
	data, _ := json.Marshal(status)
	_ = os.WriteFile(filepath.Join(filepath.Dir(path), "update-status.json"), data, 0o600)
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
	}
	return nil
}

func restorePending(path, updateID string) (pendingUpdate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pendingUpdate{}, err
	}
	var pending pendingUpdate
	if json.Unmarshal(data, &pending) != nil || pending.UpdateID != updateID ||
		!filepath.IsAbs(pending.Previous) || !filepath.IsAbs(pending.CurrentLink) {
		return pendingUpdate{}, errors.New("pending update rollback record is invalid")
	}
	if pending.NextSHA256 != "" {
		releasesRoot := filepath.Join(filepath.Dir(path), "releases")
		if !pendingReleaseTarget(releasesRoot, pending.Previous) ||
			!pendingReleaseTarget(releasesRoot, pending.Next) {
			return pendingUpdate{}, errors.New("pending update release target is invalid")
		}
	}
	previousBinary := pending.Previous
	if info, err := os.Stat(pending.Previous); err == nil && info.IsDir() {
		previousBinary = releaseBinary(pending.Previous)
	}
	previousSHA256, err := binaryidentity.SHA256(previousBinary)
	if err != nil || pending.PreviousSHA256 != "" && pending.PreviousSHA256 != previousSHA256 {
		return pendingUpdate{}, errors.New("pending update rollback binary is invalid")
	}
	if err := replaceSymlink(pending.CurrentLink, pending.Previous); err != nil {
		return pendingUpdate{}, err
	}
	_ = os.Remove(path)
	return pending, nil
}

func pendingReleaseTarget(releasesRoot, target string) bool {
	if !filepath.IsAbs(releasesRoot) || !filepath.IsAbs(target) {
		return false
	}
	release, ok := releaseEntryPath(filepath.Clean(releasesRoot), filepath.Clean(target))
	return ok && pathWithin(filepath.Clean(release), filepath.Clean(target))
}

func ConfirmInstalled(installRoot, version string, binarySHA256 ...string) error {
	path := filepath.Join(installRoot, "update-pending.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending pendingUpdate
	if json.Unmarshal(data, &pending) != nil {
		return errors.New("pending update record is invalid")
	}
	if pending.NextSHA256 == "" {
		return errors.New("legacy pending update cannot be confirmed without binary identity")
	}
	if len(binarySHA256) != 1 || len(binarySHA256[0]) != 64 {
		return errors.New("running binary identity is required to confirm update")
	}
	releasesRoot := filepath.Join(installRoot, "releases")
	if !pendingReleaseTarget(releasesRoot, pending.Previous) ||
		!pendingReleaseTarget(releasesRoot, pending.Next) {
		return errors.New("pending update release target is invalid")
	}
	runningSHA256 := binarySHA256[0]
	current, err := filepath.EvalSymlinks(pending.CurrentLink)
	if err != nil {
		return errors.New("resolve pending update activation")
	}
	if pending.PreviousSHA256 != "" && runningSHA256 == pending.PreviousSHA256 &&
		sameActivationTarget(current, pending.Previous) {
		return os.Remove(path)
	}
	if pending.Version != version || runningSHA256 != pending.NextSHA256 ||
		!sameActivationTarget(current, pending.Next) {
		return errors.New("pending update does not match running version")
	}
	return os.Remove(path)
}

func sameActivationTarget(resolved, target string) bool {
	resolvedTarget, err := filepath.EvalSymlinks(target)
	return err == nil && filepath.Clean(resolved) == filepath.Clean(resolvedTarget)
}
