package localarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func readBundle(content []byte) (Artifact, []inboxFile, error) {
	if len(content) > maxBundleBytes {
		return Artifact{}, nil, errors.New("native archive bundle exceeds 32 MiB")
	}
	container, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return Artifact{}, nil, fmt.Errorf("open native archive bundle: %w", err)
	}
	if len(container.File) > maxInboxFiles+1 {
		return Artifact{}, nil, errors.New("native archive bundle has too many entries")
	}
	var artifact Artifact
	metadataFound := false
	files := make([]inboxFile, 0, len(container.File)-1)
	names := make(map[string]struct{}, len(container.File))
	portableNames := make(map[string]struct{}, len(container.File))
	var total uint64
	for _, entry := range container.File {
		if _, duplicate := names[entry.Name]; duplicate {
			return Artifact{}, nil, fmt.Errorf("duplicate archive entry %q", entry.Name)
		}
		names[entry.Name] = struct{}{}
		if !entry.Mode().IsRegular() {
			return Artifact{}, nil, fmt.Errorf("archive entry %q is not a regular file", entry.Name)
		}
		if entry.UncompressedSize64 > maxBundlePayload-total {
			return Artifact{}, nil, errors.New("native archive uncompressed payload exceeds 30 MiB")
		}
		total += entry.UncompressedSize64
		switch {
		case entry.Name == "session.json":
			if metadataFound || entry.UncompressedSize64 > maxBundleBytes {
				return Artifact{}, nil, errors.New("invalid session metadata entry")
			}
			data, err := readZipEntry(entry, maxBundleBytes)
			if err != nil {
				return Artifact{}, nil, err
			}
			if err := decodeSessionMetadata(data, &artifact); err != nil {
				return Artifact{}, nil, err
			}
			metadataFound = true
		case strings.HasPrefix(entry.Name, "inbox/"):
			name, err := safeInboxName(strings.TrimPrefix(entry.Name, "inbox/"))
			if err != nil {
				return Artifact{}, nil, err
			}
			portableName := strings.ToLower(name)
			if _, duplicate := portableNames[portableName]; duplicate {
				return Artifact{}, nil, fmt.Errorf("duplicate portable inbox path %q", name)
			}
			portableNames[portableName] = struct{}{}
			if entry.UncompressedSize64 > maxInboxFileBytes {
				return Artifact{}, nil, fmt.Errorf("inbox entry %q exceeds size limit", name)
			}
			data, err := readZipEntry(entry, maxInboxFileBytes)
			if err != nil {
				return Artifact{}, nil, err
			}
			files = append(files, inboxFile{name: name, data: data})
		default:
			return Artifact{}, nil, fmt.Errorf("unexpected archive entry %q", entry.Name)
		}
	}
	if !metadataFound || artifact.Version != artifactVersionV2 {
		return Artifact{}, nil, errors.New("native archive bundle is missing v2 session metadata")
	}
	return artifact, files, nil
}

func decodeSessionMetadata(data []byte, destination *Artifact) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode session metadata: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("session metadata contains trailing JSON")
	}
	return nil
}

func readZipEntry(entry *zip.File, limit int64) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("open archive entry %q: %w", entry.Name, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, limit+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("read archive entry %q: %w", entry.Name, errors.Join(readErr, closeErr))
	}
	if int64(len(data)) > limit || uint64(len(data)) != entry.UncompressedSize64 {
		return nil, fmt.Errorf("archive entry %q exceeds declared limits", entry.Name)
	}
	return data, nil
}

func restoreInbox(ctx context.Context, workdir string, files []inboxFile) error {
	if !filepath.IsAbs(workdir) {
		return errors.New("session workdir must be absolute")
	}
	workInfo, err := os.Stat(workdir)
	if err != nil || !workInfo.IsDir() {
		return errors.New("session workdir is unavailable")
	}
	root := filepath.Join(workdir, inboxDirectory)
	if err := ensureSafeDirectory(root); err != nil {
		return err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		target, err := inboxTarget(root, file.name)
		if err != nil {
			return err
		}
		if err := preflightTarget(root, target, file.data); err != nil {
			return err
		}
	}
	for _, file := range files {
		target, _ := inboxTarget(root, file.name)
		if _, err := os.Lstat(target); err == nil {
			if err := preflightTarget(root, target, file.data); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := createSafeParents(root, filepath.Dir(target)); err != nil {
			return err
		}
		if err := writeNewInboxFile(target, file.data); err != nil {
			return err
		}
	}
	return nil
}

func ensureSafeDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create inbox directory: %w", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil || !info.IsDir() {
		return errors.New("inbox destination is not a safe directory")
	}
	return nil
}

func inboxTarget(root, name string) (string, error) {
	name, err := safeInboxName(name)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(name))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("inbox entry escapes destination")
	}
	return target, nil
}

func preflightTarget(root, target string, wanted []byte) error {
	if err := inspectParentChain(root, filepath.Dir(target)); err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxInboxFileBytes {
		return fmt.Errorf("refusing to overwrite unsafe inbox path %s", target)
	}
	existing, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(existing, wanted) {
		return fmt.Errorf("refusing to overwrite existing inbox file %s", target)
	}
	return nil
}

func inspectParentChain(root, directory string) error {
	for current := directory; current != root; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			return errors.New("inbox destination contains an unsafe parent")
		}
	}
	return nil
}

func createSafeParents(root, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		if err := ensureSafeDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func writeNewInboxFile(target string, data []byte) error {
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create restored inbox file: %w", err)
	}
	writeErr := error(nil)
	if _, err := file.Write(data); err != nil {
		writeErr = err
	} else {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("write restored inbox file: %w", err)
	}
	return nil
}
