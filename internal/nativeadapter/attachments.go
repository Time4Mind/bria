package nativeadapter

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"bria/internal/domain"
	"bria/internal/nativephotostaging"
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
		if err := nativephotostaging.VerifyReference(attachment.Path, attachment.Size, attachment.SHA256); err != nil {
			return "", err
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
	a.restoreMediaDirectory()
	store := nativephotostaging.Store{Directory: a.mediaDir}
	path, err := store.Stage(ctx, attachment.Path, attachment.Size, attachment.SHA256)
	a.mediaDir = store.Directory
	return path, err
}

func (a *adapter) restoreMediaDirectory() {
	if a.config.Persistent && a.config.Provider == domain.ProviderClaude {
		a.mediaDir = nativephotostaging.PersistentDirectory(a.config.StateDir, a.id)
	}
}

func (a *adapter) cleanupAttachments() error {
	store := nativephotostaging.Store{Directory: a.mediaDir}
	err := store.Cleanup()
	a.mediaDir = store.Directory
	return err
}
