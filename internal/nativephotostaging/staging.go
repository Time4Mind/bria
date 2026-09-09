// Package nativephotostaging owns verified native-input file custody across
// observer lifetimes. It has no provider policy or terminal access.
package nativephotostaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bria/internal/nativeattachment"
)

type Store struct{ Directory string }

func PersistentDirectory(stateDir, sessionID string) string {
	key := sha256.Sum256([]byte(sessionID))
	return filepath.Join(stateDir, "bria-native-media-"+hex.EncodeToString(key[:]))
}

// VerifyReference preserves the original bounded raw-file custody contract.
func VerifyReference(path string, size int64, digest string) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("native attachment unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("native attachment changed")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, size+1))
	if err != nil || n != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("native attachment changed")
	}
	return nil
}

func (s *Store) Stage(ctx context.Context, source string, size int64, digest string) (string, error) {
	data, ext, err := nativeattachment.ReadPhoto(ctx, source, size, digest)
	if err != nil {
		return "", err
	}
	if s.Directory == "" {
		s.Directory, err = os.MkdirTemp("", "bria-native-media-")
	} else {
		err = os.Mkdir(s.Directory, 0700)
		if os.IsExist(err) {
			err = nil
		}
	}
	if err != nil {
		return "", errors.New("native photo staging unavailable")
	}
	info, err := os.Lstat(s.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", errors.New("native photo staging permissions failed")
	}
	path := filepath.Join(s.Directory, digest+ext)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		if _, _, err := nativeattachment.ReadPhoto(ctx, path, size, digest); err != nil {
			return "", err
		}
		return path, nil
	}
	if err != nil {
		return "", errors.New("native photo staging unavailable")
	}
	_, writeErr := file.Write(data)
	syncErr, closeErr := file.Sync(), file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return "", errors.New("native photo staging failed")
	}
	if err := os.Chmod(path, 0400); err != nil {
		return "", errors.New("native photo staging permissions failed")
	}
	return path, nil
}

// Cleanup is explicit: a detached observer must not call it.
func (s *Store) Cleanup() error {
	if s.Directory == "" {
		return nil
	}
	if !filepath.IsAbs(s.Directory) || !strings.HasPrefix(filepath.Base(s.Directory), "bria-native-media-") {
		return errors.New("native photo cleanup identity invalid")
	}
	if err := os.RemoveAll(s.Directory); err != nil {
		return errors.New("native photo cleanup failed")
	}
	s.Directory = ""
	return nil
}
