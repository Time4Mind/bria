package archive

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
	"slices"
)

const maxArtifactBytes = 32 << 20
const maxManifestBytes = 64 << 10

type FileStore struct {
	root string
}

func NewFileStore(root string) (*FileStore, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("archive root must be absolute")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create archive root: %w", err)
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) Commit(ctx context.Context, manifest Manifest, content io.Reader) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if content == nil || manifest.Artifact.SizeBytes > maxArtifactBytes {
		return errors.New("archive artifact is missing or too large")
	}
	destination := s.archiveDir(manifest.ID)
	if existing, _, err := s.Load(ctx, manifest.ID); err == nil {
		if sameManifest(existing, manifest) {
			return nil
		}
		return errors.New("archive id already contains different content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	staging, err := os.MkdirTemp(s.root, ".bria-archive-")
	if err != nil {
		return fmt.Errorf("create archive staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := writeArtifact(ctx, filepath.Join(staging, "artifact.json"), manifest, content); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode archive manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeSyncedFile(filepath.Join(staging, "manifest.json"), encoded); err != nil {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		if existing, _, loadErr := s.Load(ctx, manifest.ID); loadErr == nil &&
			sameManifest(existing, manifest) {
			return nil
		}
		return fmt.Errorf("publish archive: %w", err)
	}
	return syncDirectory(s.root)
}

func (s *FileStore) Load(ctx context.Context, id ArchiveID) (Manifest, []byte, error) {
	if err := validateArchiveID(id); err != nil {
		return Manifest{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, nil, err
	}
	directory := s.archiveDir(id)
	manifestBytes, err := readLimitedRegular(
		filepath.Join(directory, "manifest.json"), maxManifestBytes,
	)
	if err != nil {
		return Manifest{}, nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return Manifest{}, nil, fmt.Errorf("decode archive manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil || manifest.ID != id {
		return Manifest{}, nil, errors.New("archive manifest is invalid")
	}
	if manifest.Artifact.SizeBytes > maxArtifactBytes {
		return Manifest{}, nil, errors.New("archive artifact is too large")
	}
	content, err := readLimitedRegular(
		filepath.Join(directory, "artifact.json"), manifest.Artifact.SizeBytes,
	)
	if err != nil {
		return Manifest{}, nil, err
	}
	if int64(len(content)) != manifest.Artifact.SizeBytes ||
		manifest.Artifact.Integrity != SHA256Digest(content) {
		return Manifest{}, nil, errors.New("archive artifact integrity check failed")
	}
	return manifest, content, nil
}

func readLimitedRegular(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("archive file is unsafe or too large")
	}
	return os.ReadFile(path)
}

func (s *FileStore) MarkReady(id ArchiveID) error {
	if err := validateArchiveID(id); err != nil {
		return err
	}
	directory := s.archiveDir(id)
	if _, _, err := s.Load(context.Background(), id); err != nil {
		return err
	}
	path := filepath.Join(directory, "ready")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create archive ready marker: %w", err)
	}
	return errors.Join(file.Sync(), file.Close(), syncDirectory(directory))
}

func (s *FileStore) ReadyIDs() ([]ArchiveID, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read archive root: %w", err)
	}
	ids := make([]ArchiveID, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || validateArchiveID(ArchiveID(entry.Name())) != nil {
			continue
		}
		info, statErr := os.Lstat(filepath.Join(s.root, entry.Name(), "ready"))
		if statErr == nil && info.Mode().IsRegular() {
			ids = append(ids, ArchiveID(entry.Name()))
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (s *FileStore) archiveDir(id ArchiveID) string {
	return filepath.Join(s.root, string(id))
}

func sameManifest(left, right Manifest) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func writeArtifact(ctx context.Context, path string, manifest Manifest, content io.Reader) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create archive artifact: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(content, maxArtifactBytes+1))
	if copyErr == nil && written != manifest.Artifact.SizeBytes {
		copyErr = errors.New("archive artifact size mismatch")
	}
	if copyErr == nil && hex.EncodeToString(hash.Sum(nil)) != manifest.Artifact.Integrity.Hex {
		copyErr = errors.New("archive artifact digest mismatch")
	}
	if copyErr == nil {
		copyErr = ctx.Err()
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(copyErr, closeErr)
}

func writeSyncedFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create archive manifest: %w", err)
	}
	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}
