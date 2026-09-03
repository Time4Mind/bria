package updateruntime

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

	"bria/internal/update"
	"bria/internal/updateflow"
)

var ErrInvalidStage = errors.New("invalid local update stage")

const maxStageReceiptBytes int64 = 64 << 10

var stageLocks sync.Map

type LocalStager struct {
	directory string
	maximum   int64
}

func OpenLocalStager(directory string, maximum int64) (*LocalStager, error) {
	if !filepath.IsAbs(directory) || maximum <= 0 {
		return nil, ErrInvalidStage
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create update stage directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(directory))
	if err != nil {
		return nil, fmt.Errorf("resolve update stage directory: %w", err)
	}
	if !privateRuntimeDirectory(resolved) {
		return nil, ErrInvalidStage
	}
	return &LocalStager{directory: resolved, maximum: maximum}, nil
}

func (s *LocalStager) Stage(ctx context.Context, request updateflow.StageRequest) (updateflow.StageReceipt, error) {
	if s == nil || s.directory == "" || ctx.Err() != nil || invalidStageRequest(request, s.maximum) {
		return updateflow.StageReceipt{}, ErrInvalidStage
	}
	temporary, err := os.CreateTemp(s.directory, ".stage-payload-*")
	if err != nil {
		return updateflow.StageReceipt{}, fmt.Errorf("create staged artifact: %w", err)
	}
	temporaryPath := temporary.Name()
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return updateflow.StageReceipt{}, fmt.Errorf("protect staged artifact: %w", err)
	}
	written, err := io.Copy(temporary, io.LimitReader(request.Content, s.maximum+1))
	if err != nil || written != request.Artifact.Size || written > s.maximum {
		return updateflow.StageReceipt{}, ErrInvalidStage
	}
	if err := temporary.Sync(); err != nil {
		return updateflow.StageReceipt{}, fmt.Errorf("sync staged artifact: %w", err)
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return updateflow.StageReceipt{}, fmt.Errorf("rewind staged artifact: %w", err)
	}
	if err := update.VerifyArtifact(temporary, request.Artifact); err != nil {
		return updateflow.StageReceipt{}, err
	}
	if err := temporary.Close(); err != nil {
		return updateflow.StageReceipt{}, fmt.Errorf("close staged artifact: %w", err)
	}
	temporaryOpen = false

	digest := sha256.Sum256([]byte(request.OperationID))
	base := hex.EncodeToString(digest[:])
	releasePath := filepath.Join(s.directory, base+".release")
	artifactPath := filepath.Join(releasePath, request.Artifact.Name)
	manifestPath := filepath.Join(releasePath, "release-manifest.json")
	receiptPath := filepath.Join(s.directory, base+".json")
	lock := stageLockFor(receiptPath)
	lock.Lock()
	defer lock.Unlock()
	receipt := updateflow.StageReceipt{
		OperationID: request.OperationID, NodeID: request.NodeID, Version: request.Version,
		Artifact: request.Artifact, Reference: releasePath,
	}
	var persisted updateflow.StageReceipt
	err = withStageFileLock(receiptPath+".lock", func() error {
		var persistErr error
		persisted, persistErr = s.persistStage(temporaryPath, artifactPath, manifestPath, receiptPath, request.SignedManifest, receipt)
		return persistErr
	})
	return persisted, err
}

func (s *LocalStager) persistStage(temporaryPath, artifactPath, manifestPath, receiptPath string, signedManifest []byte, receipt updateflow.StageReceipt) (updateflow.StageReceipt, error) {
	if existing, found, err := loadStageReceipt(receiptPath); err != nil {
		return updateflow.StageReceipt{}, err
	} else if found {
		if !reflect.DeepEqual(existing, receipt) || verifyStagedArtifact(artifactPath, receipt.Artifact) != nil ||
			verifyStagedManifest(manifestPath, signedManifest) != nil {
			return updateflow.StageReceipt{}, ErrInvalidStage
		}
		return existing, nil
	}
	if err := ensureStageReleaseDirectory(receipt.Reference); err != nil {
		return updateflow.StageReceipt{}, err
	}
	if _, err := os.Lstat(artifactPath); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(temporaryPath, artifactPath); err != nil {
			return updateflow.StageReceipt{}, fmt.Errorf("promote staged artifact: %w", err)
		}
		if err := syncRuntimeDirectory(filepath.Dir(artifactPath)); err != nil {
			return updateflow.StageReceipt{}, fmt.Errorf("sync update stage directory: %w", err)
		}
	} else if err != nil || verifyStagedArtifact(artifactPath, receipt.Artifact) != nil {
		return updateflow.StageReceipt{}, ErrInvalidStage
	}
	if err := ensureStagedManifest(manifestPath, signedManifest); err != nil {
		return updateflow.StageReceipt{}, err
	}
	if err := writeStageReceipt(s.directory, receiptPath, receipt); err != nil {
		return updateflow.StageReceipt{}, err
	}
	persisted, found, err := loadStageReceipt(receiptPath)
	if err != nil || !found || !reflect.DeepEqual(persisted, receipt) {
		return updateflow.StageReceipt{}, ErrInvalidStage
	}
	return persisted, nil
}

func invalidStageRequest(request updateflow.StageRequest, maximum int64) bool {
	return request.Content == nil || invalidText(request.OperationID, 256) || invalidText(request.NodeID, 1024) ||
		invalidText(request.Version, 128) || len(request.SignedManifest) == 0 || int64(len(request.SignedManifest)) > maxStageReceiptBytes ||
		request.Artifact.Name == "" || request.Artifact.Name != filepath.Base(request.Artifact.Name) ||
		request.Artifact.Size < 0 || request.Artifact.Size > maximum
}

func ensureStageReleaseDirectory(path string) error {
	err := os.Mkdir(path, 0o700)
	created := err == nil
	if err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create staged release directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidStage
	}
	if created {
		return syncRuntimeDirectory(filepath.Dir(path))
	}
	return nil
}

func ensureStagedManifest(path string, signedManifest []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return verifyStagedManifest(path, signedManifest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrInvalidStage
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".stage-manifest-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(signedManifest); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncRuntimeDirectory(filepath.Dir(path))
}

func verifyStagedManifest(path string, signedManifest []byte) error {
	content, err := readRegularBounded(path, maxStageReceiptBytes)
	if err != nil || !bytes.Equal(content, signedManifest) {
		return ErrInvalidStage
	}
	return nil
}

func invalidText(value string, maximum int) bool {
	return value == "" || len(value) > maximum || value != strings.TrimSpace(value) || strings.ContainsRune(value, 0)
}

func stageLockFor(path string) *sync.Mutex {
	value, _ := stageLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func verifyStagedArtifact(path string, artifact update.Artifact) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Size {
		return ErrInvalidStage
	}
	file, err := os.Open(path)
	if err != nil {
		return ErrInvalidStage
	}
	defer file.Close()
	return update.VerifyArtifact(file, artifact)
}

func writeStageReceipt(directory, path string, receipt updateflow.StageReceipt) (returnErr error) {
	encoded, err := json.Marshal(receipt)
	if err != nil || int64(len(encoded)) > maxStageReceiptBytes {
		return ErrInvalidStage
	}
	temporary, err := os.CreateTemp(directory, ".stage-receipt-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		if open {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	open = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncRuntimeDirectory(directory)
}

func loadStageReceipt(path string) (updateflow.StageReceipt, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return updateflow.StageReceipt{}, false, nil
	}
	if err != nil || int64(len(data)) > maxStageReceiptBytes {
		return updateflow.StageReceipt{}, false, ErrInvalidStage
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt updateflow.StageReceipt
	if err := decoder.Decode(&receipt); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return updateflow.StageReceipt{}, false, ErrInvalidStage
	}
	return receipt, true, nil
}
