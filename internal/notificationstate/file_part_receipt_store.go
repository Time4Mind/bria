package notificationstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	partReceiptFileVersion = 2
	maxPartReceiptFileSize = 8 << 20
)

type storedPart struct {
	State     DeliveryState `json:"state"`
	MessageID int64         `json:"message_id,omitempty"`
}

type partReceiptFileState struct {
	Version    int                              `json:"version"`
	Operations map[string]map[string]storedPart `json:"operations"`
	Plans      map[string]savedPlan             `json:"plans,omitempty"`
	Checksum   string                           `json:"checksum,omitempty"`
}

type FilePartReceiptStore struct{ path string }

var partReceiptLocks sync.Map

func OpenFilePartReceiptStore(path string) (*FilePartReceiptStore, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, errors.New("part receipt store path must be absolute")
	}
	store := &FilePartReceiptStore{path: filepath.Clean(path)}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return nil, err
	}
	if err := store.inspect(func(partReceiptFileState) error { return nil }); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FilePartReceiptStore) ConfirmedParts(ctx context.Context, operationID string) ([]PartReceipt, error) {
	var result []PartReceipt
	err := store.inspect(func(state partReceiptFileState) error {
		for partID, part := range state.Operations[operationID] {
			if part.State == DeliveryConfirmed {
				result = append(result, PartReceipt{PartID: partID, MessageID: part.MessageID})
			}
		}
		sort.Slice(result, func(left, right int) bool { return result[left].PartID < result[right].PartID })
		return nil
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result, err
}

func (store *FilePartReceiptStore) UnknownParts(ctx context.Context, operationID string) ([]string, error) {
	var result []string
	err := store.inspect(func(state partReceiptFileState) error {
		for partID, part := range state.Operations[operationID] {
			if part.State == DeliveryUnknown {
				result = append(result, partID)
			}
		}
		sort.Strings(result)
		return nil
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result, err
}

func (store *FilePartReceiptStore) ConfirmPart(ctx context.Context, operationID string, receipt PartReceipt) error {
	if receipt.MessageID <= 0 {
		return errors.New("confirmed Telegram part requires a positive message receipt")
	}
	return store.replace(ctx, operationID, receipt.PartID, storedPart{State: DeliveryConfirmed, MessageID: receipt.MessageID}, false)
}

func (store *FilePartReceiptStore) MarkPartUnknown(ctx context.Context, operationID, partID string) error {
	return store.replace(ctx, operationID, partID, storedPart{State: DeliveryUnknown}, false)
}

// ResolveUnknownConfirmed is an explicit owner reconciliation using a
// separately verified Telegram message receipt.
func (store *FilePartReceiptStore) ResolveUnknownConfirmed(ctx context.Context, operationID string, receipt PartReceipt) error {
	if receipt.MessageID <= 0 {
		return errors.New("resolved Telegram part requires a positive message receipt")
	}
	return store.replace(ctx, operationID, receipt.PartID, storedPart{State: DeliveryConfirmed, MessageID: receipt.MessageID}, true)
}

// ResolveUnknownForRetry explicitly removes only the selected unknown fence;
// confirmed parts remain immutable and are never sent again.
func (store *FilePartReceiptStore) ResolveUnknownForRetry(ctx context.Context, operationID, partID string) error {
	return store.mutate(ctx, func(state *partReceiptFileState) error {
		if err := validateStoredBinding(*state, operationID, partID, false); err != nil {
			return err
		}
		parts := state.Operations[operationID]
		if parts == nil || parts[partID].State != DeliveryUnknown {
			return errors.New("Telegram part is not unknown")
		}
		delete(parts, partID)
		return nil
	})
}

func (store *FilePartReceiptStore) replace(ctx context.Context, operationID, partID string, next storedPart, requireUnknown bool) error {
	return store.mutate(ctx, func(state *partReceiptFileState) error {
		if err := validateStoredBinding(*state, operationID, partID, false); err != nil {
			return err
		}
		parts := state.Operations[operationID]
		if parts == nil {
			parts = make(map[string]storedPart)
			state.Operations[operationID] = parts
		}
		current, exists := parts[partID]
		if requireUnknown && (!exists || current.State != DeliveryUnknown) {
			return errors.New("Telegram part is not unknown")
		}
		if exists && current.State == DeliveryConfirmed {
			if current == next {
				return nil
			}
			return errors.New("confirmed Telegram part receipt conflicts")
		}
		parts[partID] = next
		return nil
	})
}

func (store *FilePartReceiptStore) mutate(ctx context.Context, action func(*partReceiptFileState) error) error {
	if store == nil || store.path == "" || ctx.Err() != nil {
		return errors.New("part receipt store unavailable")
	}
	lock := partReceiptProcessLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	return withPartReceiptFileLock(store.path+".lock", func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := readPartReceiptState(store.path)
		if err != nil {
			return err
		}
		if err := action(&state); err != nil {
			if errors.Is(err, errNoChange) {
				return nil
			}
			return err
		}
		if state.Version == partReceiptFileVersion {
			state.Checksum = stateChecksum(state)
		}
		if err := writePartReceiptState(store.path, state); err != nil {
			return err
		}
		verified, err := readPartReceiptState(store.path)
		if err != nil || !reflect.DeepEqual(verified, state) {
			return errors.New("part receipt store reread mismatch")
		}
		return nil
	})
}

func (store *FilePartReceiptStore) inspect(action func(partReceiptFileState) error) error {
	if store == nil || store.path == "" {
		return errors.New("part receipt store unavailable")
	}
	lock := partReceiptProcessLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	return withPartReceiptFileLock(store.path+".lock", func() error {
		state, err := readPartReceiptState(store.path)
		if err != nil {
			return err
		}
		return action(state)
	})
}

func partReceiptProcessLock(path string) *sync.Mutex {
	value, _ := partReceiptLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func validatePartBinding(operationID, partID string) error {
	if validateOperation(operationID) != nil ||
		strings.TrimSpace(partID) == "" || len(partID) > 768 || !strings.HasPrefix(partID, operationID+":part:") {
		return errors.New("invalid Telegram part identity")
	}
	index, total, ok := strings.Cut(strings.TrimPrefix(partID, operationID+":part:"), "-of-")
	part, partErr := strconv.Atoi(index)
	count, countErr := strconv.Atoi(total)
	if !ok || partErr != nil || countErr != nil || part < 1 || count < part || strconv.Itoa(part) != index || strconv.Itoa(count) != total {
		return errors.New("invalid Telegram part identity")
	}
	return nil
}

func readPartReceiptState(path string) (partReceiptFileState, error) {
	state := partReceiptFileState{Version: 1, Operations: make(map[string]map[string]storedPart)}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPartReceiptFileSize {
		return partReceiptFileState{}, errors.New("invalid part receipt store file")
	}
	file, err := os.Open(path)
	if err != nil {
		return partReceiptFileState{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPartReceiptFileSize+1))
	if err != nil || len(data) > maxPartReceiptFileSize || !utf8.Valid(data) || rejectDuplicateKeys(data) != nil {
		return partReceiptFileState{}, errors.New("invalid part receipt store file")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	state = partReceiptFileState{}
	if err := decoder.Decode(&state); err != nil {
		return partReceiptFileState{}, errors.New("invalid part receipt store file")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || validateFileState(state) != nil {
		return partReceiptFileState{}, errors.New("invalid part receipt store file")
	}
	return state, nil
}

func validateFileState(state partReceiptFileState) error {
	if (state.Version == 1 && state.Checksum != "") ||
		(state.Version == partReceiptFileVersion && state.Checksum != stateChecksum(state)) {
		return errors.New("invalid part receipt store checksum")
	}
	if (state.Version != 1 && state.Version != partReceiptFileVersion) || state.Operations == nil ||
		(state.Version == 1 && state.Plans != nil) || (state.Version == 2 && state.Plans == nil) {
		return errors.New("invalid part receipt store version")
	}
	for operationID, parts := range state.Operations {
		if parts == nil || validateOperation(operationID) != nil {
			return errors.New("invalid part receipt store operation")
		}
		total := ""
		for partID, part := range parts {
			_, currentTotal, _ := strings.Cut(strings.TrimPrefix(partID, operationID+":part:"), "-of-")
			if validateStoredBinding(state, operationID, partID, false) != nil ||
				(total != "" && currentTotal != total) ||
				(part.State != DeliveryConfirmed && part.State != DeliveryUnknown) ||
				(part.State == DeliveryConfirmed && part.MessageID <= 0) || (part.State == DeliveryUnknown && part.MessageID != 0) {
				return errors.New("invalid part receipt store part")
			}
			total = currentTotal
		}
	}
	for operationID, plan := range state.Plans {
		if validateOperation(operationID) != nil || validateSavedPlan(operationID, plan) != nil {
			return errors.New("invalid part receipt store plan")
		}
	}
	return nil
}

func writePartReceiptState(path string, state partReceiptFileState) error {
	if err := validateFileState(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil || len(data) > maxPartReceiptFileSize {
		return errors.New("part receipt store is too large")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".telegram-parts-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}
