package artifactdelivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"bria/internal/files"
)

const maxManifestBytes int64 = 1 << 20

var manifestLocks sync.Map

// FileStore persists one delivery manifest per provider final.
type FileStore struct {
	directory string
}

// OpenFileStore opens a durable manifest directory.
func OpenFileStore(directory string) (*FileStore, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, ErrInvalidService
	}
	directory = filepath.Clean(directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact manifest directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact manifest directory: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return nil, ErrInvalidService
	}
	return &FileStore{directory: resolved}, nil
}

// Load reads and validates a manifest.
func (s *FileStore) Load(ctx context.Context, finalID string) (files.DeliveryManifest, bool, error) {
	if s == nil || s.directory == "" || invalidFinalID(finalID) {
		return files.DeliveryManifest{}, false, ErrInvalidService
	}
	if err := ctx.Err(); err != nil {
		return files.DeliveryManifest{}, false, err
	}
	path := s.path(finalID)
	lock := lockFor(path)
	lock.Lock()
	defer lock.Unlock()
	manifest, found, err := loadManifest(path, finalID)
	if err != nil {
		return files.DeliveryManifest{}, false, err
	}
	return manifest, found, nil
}

// Save atomically persists a manifest and verifies the promoted bytes by
// reopening the destination before returning.
func (s *FileStore) Save(ctx context.Context, manifest files.DeliveryManifest) (returnErr error) {
	if s == nil || s.directory == "" || invalidFinalID(manifest.FinalID) {
		return ErrInvalidService
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("validate artifact manifest: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode artifact manifest: %w", err)
	}
	if int64(len(encoded)) > maxManifestBytes {
		return files.ErrInvalidManifest
	}

	path := s.path(manifest.FinalID)
	lock := lockFor(path)
	lock.Lock()
	defer lock.Unlock()

	temporary, err := os.CreateTemp(s.directory, ".manifest-*")
	if err != nil {
		return fmt.Errorf("create artifact manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			if closeErr := temporary.Close(); returnErr == nil && closeErr != nil {
				returnErr = fmt.Errorf("close artifact manifest: %w", closeErr)
			}
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect artifact manifest: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fmt.Errorf("write artifact manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync artifact manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close artifact manifest: %w", err)
	}
	temporaryOpen = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("promote artifact manifest: %w", err)
	}
	if err := syncDirectory(s.directory); err != nil {
		return fmt.Errorf("sync artifact manifest directory: %w", err)
	}
	persisted, found, err := loadManifest(path, manifest.FinalID)
	if err != nil || !found {
		if err == nil {
			err = ErrManifestAbsent
		}
		return fmt.Errorf("verify artifact manifest: %w", err)
	}
	if !reflect.DeepEqual(persisted, manifest) {
		return errors.New("verify artifact manifest: persisted value differs")
	}
	return nil
}

func (s *FileStore) path(finalID string) string {
	hash := sha256.Sum256([]byte(finalID))
	return filepath.Join(s.directory, hex.EncodeToString(hash[:])+".json")
}

func lockFor(path string) *sync.Mutex {
	value, _ := manifestLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func loadManifest(path, finalID string) (files.DeliveryManifest, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return files.DeliveryManifest{}, false, nil
	}
	if err != nil {
		return files.DeliveryManifest{}, false, fmt.Errorf("open artifact manifest: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return files.DeliveryManifest{}, false, fmt.Errorf("inspect artifact manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return files.DeliveryManifest{}, false, files.ErrInvalidManifest
	}
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil || int64(len(data)) > maxManifestBytes {
		return files.DeliveryManifest{}, false, files.ErrInvalidManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest files.DeliveryManifest
	if err := decoder.Decode(&manifest); err != nil {
		return files.DeliveryManifest{}, false, files.ErrInvalidManifest
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return files.DeliveryManifest{}, false, files.ErrInvalidManifest
	}
	if manifest.FinalID != finalID {
		return files.DeliveryManifest{}, false, files.ErrInvalidManifest
	}
	if err := manifest.Validate(); err != nil {
		return files.DeliveryManifest{}, false, fmt.Errorf("validate persisted artifact manifest: %w", err)
	}
	return manifest, true, nil
}

func invalidFinalID(finalID string) bool {
	return finalID == "" || len(finalID) > 1024 || strings.ContainsRune(finalID, 0)
}
