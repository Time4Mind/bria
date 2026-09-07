package nativeadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bria/internal/domain"
	"bria/internal/nativeattachment"
	"bria/internal/runtimeprotocol"
)

// Like the native legacy interface, media is a verified file reference pasted
// with the caption, not a fabricated multimodal RPC or clipboard operation.
func (a *adapter) inputText(ctx context.Context, r runtimeprotocol.ParentMessage) (string, error) {
	parts := []string{r.Text}
	for _, attachment := range r.Attachments {
		if a.config.Provider == domain.ProviderClaude {
			path, err := a.claudePhoto(ctx, attachment)
			if err != nil {
				return "", err
			}
			parts = append(parts, "Attached image: "+strconv.Quote(path))
			continue
		}
		file, err := os.Open(attachment.Path)
		if err != nil {
			return "", errors.New("native attachment unavailable")
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != attachment.Size {
			file.Close()
			return "", errors.New("native attachment changed")
		}
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(file, attachment.Size+1))
		file.Close()
		if err != nil || n != attachment.Size || hex.EncodeToString(hash.Sum(nil)) != attachment.SHA256 {
			return "", errors.New("native attachment changed")
		}
		path := attachment.Path
		if rel, err := filepath.Rel(a.config.Workdir, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			path = filepath.ToSlash(rel)
		}
		parts = append(parts, path)
	}
	text := strings.Join(parts, "\n\n")
	if len(text) > 32<<10 {
		return "", errors.New("native input exceeds limit")
	}
	return text, nil
}

func (a *adapter) claudePhoto(ctx context.Context, attachment runtimeprotocol.LocalAttachment) (string, error) {
	data, ext, err := nativeattachment.ReadPhoto(ctx, attachment.Path, attachment.Size, attachment.SHA256)
	if err != nil {
		return "", err
	}
	if a.mediaDir == "" {
		a.mediaDir, err = os.MkdirTemp("", "bria-native-media-")
		if err != nil {
			return "", errors.New("native photo staging unavailable")
		}
	}
	path := filepath.Join(a.mediaDir, attachment.SHA256+ext)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		if _, _, verifyErr := nativeattachment.ReadPhoto(ctx, path, attachment.Size, attachment.SHA256); verifyErr != nil {
			return "", verifyErr
		}
		return path, nil
	}
	if err != nil {
		return "", errors.New("native photo staging unavailable")
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return "", errors.New("native photo staging failed")
	}
	if err := os.Chmod(path, 0400); err != nil {
		return "", errors.New("native photo staging permissions failed")
	}
	return path, nil
}

func (a *adapter) cleanupAttachments() error {
	if a.mediaDir == "" {
		return nil
	}
	if err := os.RemoveAll(a.mediaDir); err != nil {
		return errors.New("native photo cleanup failed")
	}
	a.mediaDir = ""
	return nil
}
