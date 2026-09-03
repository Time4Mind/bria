package artifactproduction

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"bria/internal/artifactdelivery"
	"bria/internal/telegram"
)

const maxIntegrityRecordBytes int64 = 16 << 10

type attemptState string

const (
	attemptPending   attemptState = "pending"
	attemptUnknown   attemptState = "unknown"
	attemptConfirmed attemptState = "confirmed"
)

type integrityRecord struct {
	Version      int          `json:"version"`
	FileID       string       `json:"file_id"`
	Size         int64        `json:"size"`
	SHA256       string       `json:"sha256"`
	Attempt      uint32       `json:"attempt"`
	OperationID  string       `json:"operation_id"`
	ChatID       int64        `json:"chat_id"`
	State        attemptState `json:"state"`
	MessageID    int64        `json:"message_id,omitempty"`
	RemoteFileID string       `json:"remote_file_id,omitempty"`
	RemoteUnique string       `json:"remote_unique_id,omitempty"`
}

type integrityStore struct{ directory string }

var integrityLocks sync.Map

func (store *integrityStore) begin(ctx context.Context, file artifactdelivery.TransportFile, chatID telegram.ChatID, content []byte) (*telegram.FileReceipt, error) {
	if err := store.validate(ctx); err != nil || !validAttempt(file, chatID, content) {
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidConfiguration
	}
	path := store.path(file.FileID)
	lock := integrityLock(path)
	lock.Lock()
	defer lock.Unlock()
	record, found, err := loadIntegrity(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(content)
	digestText := hex.EncodeToString(digest[:])
	if found {
		if record.FileID != file.FileID || record.Size != file.Size || record.SHA256 != digestText {
			return nil, ErrArtifactChanged
		}
		if file.Attempt <= record.Attempt || file.OperationID == record.OperationID {
			return nil, errors.New("artifact delivery attempt is not newer than durable custody")
		}
		if record.State == attemptConfirmed {
			record.Attempt = file.Attempt
			record.OperationID = file.OperationID
			if err := writeIntegrity(path, record); err != nil {
				return nil, err
			}
			return &telegram.FileReceipt{MessageID: telegram.MessageID(record.MessageID), ChatID: telegram.ChatID(record.ChatID), FileID: record.RemoteFileID, FileUniqueID: record.RemoteUnique}, nil
		}
	}
	next := integrityRecord{Version: 1, FileID: file.FileID, Size: file.Size, SHA256: digestText, Attempt: file.Attempt, OperationID: file.OperationID, ChatID: int64(chatID), State: attemptPending}
	return nil, writeIntegrity(path, next)
}

func (store *integrityStore) unknown(ctx context.Context, file artifactdelivery.TransportFile) error {
	return store.transition(ctx, file, func(record *integrityRecord) error {
		if record.State != attemptPending {
			return errors.New("artifact attempt is not pending")
		}
		record.State = attemptUnknown
		return nil
	})
}

func (store *integrityStore) confirm(ctx context.Context, file artifactdelivery.TransportFile, receipt telegram.FileReceipt) error {
	return store.transition(ctx, file, func(record *integrityRecord) error {
		if record.State != attemptPending || record.ChatID != int64(receipt.ChatID) || receipt.MessageID <= 0 ||
			!validRemoteIdentity(receipt.FileID) || !validRemoteIdentity(receipt.FileUniqueID) {
			return errors.New("artifact receipt does not match pending attempt")
		}
		record.State = attemptConfirmed
		record.MessageID = int64(receipt.MessageID)
		record.RemoteFileID = receipt.FileID
		record.RemoteUnique = receipt.FileUniqueID
		return nil
	})
}

func (store *integrityStore) transition(ctx context.Context, file artifactdelivery.TransportFile, mutate func(*integrityRecord) error) error {
	if err := store.validate(ctx); err != nil {
		return err
	}
	path := store.path(file.FileID)
	lock := integrityLock(path)
	lock.Lock()
	defer lock.Unlock()
	record, found, err := loadIntegrity(path)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return errors.New("artifact integrity record is absent")
	}
	if record.FileID != file.FileID || record.Attempt != file.Attempt || record.OperationID != file.OperationID {
		return errors.New("artifact attempt identity mismatch")
	}
	if err := mutate(&record); err != nil {
		return err
	}
	return writeIntegrity(path, record)
}

func (store *integrityStore) path(fileID string) string {
	digest := sha256.Sum256([]byte(fileID))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+".json")
}

func (store *integrityStore) validate(ctx context.Context) error {
	if store == nil || store.directory == "" {
		return ErrInvalidConfiguration
	}
	if ctx == nil {
		return ErrInvalidConfiguration
	}
	return ctx.Err()
}

func validAttempt(file artifactdelivery.TransportFile, chatID telegram.ChatID, content []byte) bool {
	return file.FileID != "" && len(file.FileID) <= 256 && utf8.ValidString(file.FileID) && !strings.ContainsRune(file.FileID, 0) &&
		file.Attempt > 0 && file.OperationID != "" && len(file.OperationID) <= 512 && utf8.ValidString(file.OperationID) &&
		!strings.ContainsRune(file.OperationID, 0) && chatID != 0 && file.Size >= 0 && int64(len(content)) == file.Size
}

func validRemoteIdentity(value string) bool {
	return value != "" && len(value) <= 512 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}

func integrityLock(path string) *sync.Mutex {
	value, _ := integrityLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func loadIntegrity(path string) (integrityRecord, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return integrityRecord{}, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxIntegrityRecordBytes {
		return integrityRecord{}, false, errors.New("invalid artifact integrity record")
	}
	handle, err := os.Open(path)
	if err != nil {
		return integrityRecord{}, false, err
	}
	defer handle.Close()
	data, err := io.ReadAll(io.LimitReader(handle, maxIntegrityRecordBytes+1))
	if err != nil || int64(len(data)) != info.Size() {
		return integrityRecord{}, false, errors.New("read artifact integrity record")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record integrityRecord
	if err := decoder.Decode(&record); err != nil {
		return integrityRecord{}, false, errors.New("decode artifact integrity record")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || !validIntegrityRecord(record) {
		return integrityRecord{}, false, errors.New("invalid artifact integrity record")
	}
	return record, true, nil
}

func validIntegrityRecord(record integrityRecord) bool {
	if record.Version != 1 || record.FileID == "" || record.Size < 0 || len(record.SHA256) != sha256.Size*2 ||
		record.Attempt == 0 || record.OperationID == "" || record.ChatID == 0 {
		return false
	}
	if _, err := hex.DecodeString(record.SHA256); err != nil {
		return false
	}
	switch record.State {
	case attemptPending, attemptUnknown:
		return record.MessageID == 0 && record.RemoteFileID == "" && record.RemoteUnique == ""
	case attemptConfirmed:
		return record.MessageID > 0 && validRemoteIdentity(record.RemoteFileID) && validRemoteIdentity(record.RemoteUnique)
	default:
		return false
	}
}

func writeIntegrity(path string, record integrityRecord) (returnErr error) {
	encoded, err := json.Marshal(record)
	if err != nil || int64(len(encoded)) > maxIntegrityRecordBytes {
		return errors.New("encode artifact integrity record")
	}
	return writePrivateAtomic(path, ".integrity-", encoded, func() error {
		persisted, found, err := loadIntegrity(path)
		if err != nil || !found || persisted != record {
			return errors.New("verify artifact integrity record")
		}
		return nil
	})
}
