package updateflow

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
	"sync"
)

var ErrInvalidStore = errors.New("invalid update flow store")

const maxStateBytes int64 = 4 << 20

var stateLocks sync.Map

// FileStore durably persists one independently reread state per flow.
type FileStore struct{ directory string }

func OpenFileStore(directory string) (*FileStore, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, ErrInvalidStore
	}
	directory = filepath.Clean(directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create update flow directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve update flow directory: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return nil, ErrInvalidStore
	}
	return &FileStore{directory: resolved}, nil
}

func (s *FileStore) Load(ctx context.Context, flowID string) (State, bool, error) {
	if s == nil || s.directory == "" || invalidIdentity(flowID, 1024) {
		return State{}, false, ErrInvalidStore
	}
	if err := ctx.Err(); err != nil {
		return State{}, false, err
	}
	path := s.path(flowID)
	lock := stateLockFor(path)
	lock.Lock()
	defer lock.Unlock()
	var state State
	var found bool
	err := withStateFileLock(path+".lock", func() error {
		var loadErr error
		state, found, loadErr = loadState(path, flowID)
		return loadErr
	})
	return state, found, err
}

func (s *FileStore) Save(ctx context.Context, state State) error {
	if s == nil || s.directory == "" || invalidIdentity(state.FlowID, 1024) {
		return ErrInvalidStore
	}
	if err := validateState(state); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if state.Revision == 0 {
		return ErrRevisionConflict
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode update flow state: %w", err)
	}
	if int64(len(encoded)) > maxStateBytes {
		return ErrInvalidStore
	}
	path := s.path(state.FlowID)
	lock := stateLockFor(path)
	lock.Lock()
	defer lock.Unlock()
	return withStateFileLock(path+".lock", func() error {
		current, found, err := loadState(path, state.FlowID)
		if err != nil {
			return err
		}
		if found {
			if current.Revision == state.Revision && reflect.DeepEqual(current, state) {
				return nil
			}
			if current.Revision+1 != state.Revision {
				return ErrRevisionConflict
			}
		} else if state.Revision != 1 {
			return ErrRevisionConflict
		}
		return s.writeState(path, state, encoded)
	})
}

func (s *FileStore) writeState(path string, state State, encoded []byte) (returnErr error) {
	temporary, err := os.CreateTemp(s.directory, ".update-flow-*")
	if err != nil {
		return fmt.Errorf("create update flow temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			if closeErr := temporary.Close(); returnErr == nil && closeErr != nil {
				returnErr = fmt.Errorf("close update flow state: %w", closeErr)
			}
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect update flow state: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fmt.Errorf("write update flow state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync update flow state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close update flow state: %w", err)
	}
	temporaryOpen = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("promote update flow state: %w", err)
	}
	if err := syncDirectory(s.directory); err != nil {
		return fmt.Errorf("sync update flow directory: %w", err)
	}
	persisted, found, err := loadState(path, state.FlowID)
	if err != nil || !found {
		if err == nil {
			err = ErrFlowAbsent
		}
		return fmt.Errorf("verify update flow state: %w", err)
	}
	if !reflect.DeepEqual(persisted, state) {
		return errors.New("verify update flow state: persisted value differs")
	}
	return nil
}

func (s *FileStore) path(flowID string) string {
	digest := sha256.Sum256([]byte(flowID))
	return filepath.Join(s.directory, hex.EncodeToString(digest[:])+".json")
}

func stateLockFor(path string) *sync.Mutex {
	value, _ := stateLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func loadState(path, flowID string) (State, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("open update flow state: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxStateBytes {
		return State{}, false, ErrInvalidStore
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil || int64(len(data)) > maxStateBytes {
		return State{}, false, ErrInvalidStore
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return State{}, false, ErrInvalidStore
	}
	if state.FlowID != flowID || state.Revision == 0 || validateState(state) != nil {
		return State{}, false, ErrInvalidStore
	}
	return state, true, nil
}
