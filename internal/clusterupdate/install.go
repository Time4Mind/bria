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

func verifyReleaseBinary(destination, version string) error {
	binary := releaseBinary(destination)
	output, err := exec.Command(binary, "version").Output()
	if err != nil {
		return fmt.Errorf("verify staged Bria binary: %w", err)
	}
	var value struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(output, &value) != nil || value.Version != version {
		return errors.New("staged Bria version does not match manifest")
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
	currentDirectory := false
	info, inspectErr := os.Lstat(current)
	if inspectErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(current)
		if err != nil {
			return fmt.Errorf("resolve current release: %w", err)
		}
		previous = target
	} else if inspectErr == nil && info.IsDir() {
		previous = filepath.Join(m.config.InstallRoot, "releases", "pre-update-"+safeReleaseName(request.UpdateID))
		currentDirectory = true
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
	newLink := current + ".new"
	_ = os.Remove(newLink)
	if err := os.Symlink(destinationTarget, newLink); err != nil {
		return err
	}
	if currentDirectory {
		if err := os.Rename(current, previous); err != nil {
			_ = os.Remove(newLink)
			return fmt.Errorf("migrate current release: %w", err)
		}
	}
	if err := os.Rename(newLink, current); err != nil {
		if currentDirectory {
			_ = os.Rename(previous, current)
		}
		return err
	}
	return writePending(filepath.Join(m.config.InstallRoot, "update-pending.json"), pendingUpdate{
		NodeID: request.NodeID, UpdateID: request.UpdateID, Version: request.Version,
		Previous: previous, CurrentLink: current,
	})
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode&0o777)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, 512<<20))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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
	newLink := pending.CurrentLink + ".rollback"
	_ = os.Remove(newLink)
	if err := os.Symlink(pending.Previous, newLink); err != nil {
		return pendingUpdate{}, err
	}
	if err := os.Rename(newLink, pending.CurrentLink); err != nil {
		return pendingUpdate{}, err
	}
	_ = os.Remove(path)
	return pending, nil
}

func ConfirmInstalled(installRoot, version string) error {
	path := filepath.Join(installRoot, "update-pending.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending pendingUpdate
	if json.Unmarshal(data, &pending) != nil || pending.Version != version {
		return errors.New("pending update does not match running version")
	}
	return os.Remove(path)
}
