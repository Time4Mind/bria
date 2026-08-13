package inbound

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func prepareInbox(workdir string) (string, error) {
	info, err := os.Lstat(workdir)
	if err != nil {
		return "", fmt.Errorf("inspect workdir: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", ErrUnsafePath
	}
	inbox := filepath.Join(workdir, ".bria-inbox")
	if err := os.Mkdir(inbox, 0o700); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("create inbox: %w", err)
	}
	info, err = os.Lstat(inbox)
	if err != nil {
		return "", fmt.Errorf("inspect inbox: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", ErrUnsafePath
	}
	if err := os.Chmod(inbox, 0o700); err != nil {
		return "", fmt.Errorf("secure inbox: %w", err)
	}
	return inbox, nil
}

func stableMediaName(input Input) string {
	digest := sha256.Sum256([]byte(string(input.Kind) + "\x00" + input.UniqueID))
	extension := safeExtension(input)
	return string(input.Kind) + "-" + hex.EncodeToString(digest[:12]) + extension
}

func safeExtension(input Input) string {
	extension := strings.ToLower(filepath.Ext(filepath.Base(input.FileName)))
	if len(extension) > 12 || extension == "." {
		extension = ""
	}
	for _, character := range extension {
		if character != '.' && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') {
			extension = ""
			break
		}
	}
	if extension != "" {
		return extension
	}
	if input.Kind == KindPhoto {
		return ".jpg"
	}
	return ".bin"
}

func validExistingMedia(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect inbound media: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, ErrUnsafePath
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return false, fmt.Errorf("secure existing inbound media: %w", err)
	}
	return true, nil
}

func (p *Processor) downloadAtomic(
	ctx context.Context,
	inbox string,
	destination string,
	input Input,
) error {
	temporary, err := os.CreateTemp(inbox, ".download-*")
	if err != nil {
		return fmt.Errorf("create inbound temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure inbound temporary file: %w", err)
	}
	if err := p.downloadTo(ctx, temporary, input); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync inbound media: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close inbound media: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		if exists, inspectErr := validExistingMedia(destination); inspectErr == nil && exists {
			return nil
		}
		return fmt.Errorf("publish inbound media: %w", err)
	}
	return os.Chmod(destination, 0o600)
}

func (p *Processor) downloadTo(ctx context.Context, destination io.Writer, input Input) error {
	for attempt := 0; attempt < 3; attempt++ {
		writer := &maximumWriter{destination: destination, remaining: p.maxBytes}
		written, err := p.downloader.Download(ctx, input.FileID, writer, p.maxBytes)
		if errors.Is(err, ErrMediaTooLarge) || writer.exceeded || written > p.maxBytes {
			return ErrMediaTooLarge
		}
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || attempt == 2 || !resetDownload(destination) {
			return fmt.Errorf("download inbound media: %w", err)
		}
	}
	return errors.New("download inbound media failed")
}

type resettableWriter interface {
	io.Seeker
	Truncate(int64) error
}

func resetDownload(destination io.Writer) bool {
	file, ok := destination.(resettableWriter)
	if !ok {
		return false
	}
	return file.Truncate(0) == nil && seekStart(file)
}

func seekStart(file io.Seeker) bool {
	_, err := file.Seek(0, io.SeekStart)
	return err == nil
}

type maximumWriter struct {
	destination io.Writer
	remaining   int64
	exceeded    bool
}

func (w *maximumWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		w.exceeded = true
		return 0, ErrMediaTooLarge
	}
	written, err := w.destination.Write(data)
	w.remaining -= int64(written)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	return written, err
}
