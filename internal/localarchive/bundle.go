package localarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	inboxDirectory    = ".bria-inbox"
	maxInboxFiles     = 256
	maxInboxFileBytes = 20 << 20
	// The 30 MiB limit includes session.json. The remaining 2 MiB below the
	// FileStore cap covers worst-case DEFLATE expansion plus two ZIP headers for
	// 256 paths of 1024 bytes: under 560 KiB in total framing overhead.
	maxBundlePayload  = 30 << 20
	maxBundleBytes    = 32 << 20
	maxInboxPathBytes = 1024
)

type inboxFile struct {
	name string
	data []byte
}

func buildBundle(ctx context.Context, artifact Artifact) ([]byte, error) {
	metadata, err := json.Marshal(artifact)
	if err != nil {
		return nil, fmt.Errorf("encode session metadata: %w", err)
	}
	files, err := readInbox(ctx, artifact.Workdir)
	if err != nil {
		return nil, err
	}
	payloadBytes := int64(len(metadata))
	for _, file := range files {
		payloadBytes += int64(len(file.data))
	}
	if payloadBytes > maxBundlePayload {
		return nil, errors.New("native archive uncompressed payload exceeds 30 MiB")
	}
	var output bytes.Buffer
	container := zip.NewWriter(&output)
	if err := writeZipEntry(container, "session.json", metadata); err != nil {
		return nil, err
	}
	for _, file := range files {
		if err := writeZipEntry(container, "inbox/"+file.name, file.data); err != nil {
			return nil, err
		}
	}
	if err := container.Close(); err != nil {
		return nil, fmt.Errorf("close archive bundle: %w", err)
	}
	if output.Len() > maxBundleBytes {
		return nil, errors.New("native archive bundle exceeds 32 MiB")
	}
	return output.Bytes(), nil
}

func readInbox(ctx context.Context, workdir string) ([]inboxFile, error) {
	root := filepath.Join(workdir, inboxDirectory)
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect inbox: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("inbox must be a directory, not a link or special file")
	}
	var files []inboxFile
	var total int64
	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filePath == root {
			return nil
		}
		info, err := os.Lstat(filePath)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("inbox contains non-regular file: %s", filePath)
		}
		if len(files) >= maxInboxFiles || info.Size() > maxInboxFileBytes {
			return errors.New("inbox file count or size limit exceeded")
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		name, err := safeInboxName(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		data, err := readRegularFile(filePath, info)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > maxBundlePayload {
			return errors.New("inbox total size limit exceeded")
		}
		files = append(files, inboxFile{name: name, data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collect inbox: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

func readRegularFile(filePath string, expected os.FileInfo) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		return nil, errors.New("inbox file changed while being archived")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxInboxFileBytes+1))
	if err != nil || int64(len(data)) != expected.Size() {
		return nil, errors.New("inbox file changed while being archived")
	}
	return data, nil
}

func writeZipEntry(container *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o600)
	entry, err := container.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create archive entry: %w", err)
	}
	if _, err := entry.Write(data); err != nil {
		return fmt.Errorf("write archive entry: %w", err)
	}
	return nil
}

func safeInboxName(name string) (string, error) {
	if name == "" || len(name) > maxInboxPathBytes || name == "." ||
		path.IsAbs(name) || path.Clean(name) != name ||
		strings.Contains(name, "\\") || strings.Contains(name, ":") ||
		strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
		return "", fmt.Errorf("unsafe inbox path %q", name)
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return "", fmt.Errorf("unsafe inbox path %q", name)
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." ||
			strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") {
			return "", fmt.Errorf("unsafe inbox path %q", name)
		}
	}
	return name, nil
}
