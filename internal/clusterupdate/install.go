package clusterupdate

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
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
