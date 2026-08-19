package runtimehost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var operationBucket = []byte("runtime_operations_v1")

const (
	operationPayloadRetention = 15 * time.Minute
	operationMaintenanceEvery = time.Minute
)

type BoltOperationStore struct {
	db              *bolt.DB
	now             func() time.Time
	maintenanceMu   sync.Mutex
	lastMaintenance time.Time
}

func OpenBoltOperationStore(path string) (*BoltOperationStore, error) {
	return openBoltOperationStore(path, time.Now)
}

func openBoltOperationStore(path string, now func() time.Time) (*BoltOperationStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("operation store path is required")
	}
	if now == nil {
		return nil, errors.New("operation store clock is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create operation store directory: %w", err)
	}
	db, err := openOperationDB(path)
	if err != nil {
		return nil, err
	}
	store := &BoltOperationStore{db: db, now: now}
	changed, err := store.maintain(now())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	store.lastMaintenance = now()
	if !changed {
		return store, nil
	}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("close operation store before compaction: %w", err)
	}
	if err := compactOperationDB(path); err != nil {
		return nil, err
	}
	db, err = openOperationDB(path)
	if err != nil {
		return nil, err
	}
	store.db = db
	return store, nil
}

func openOperationDB(path string) (*bolt.DB, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open operation store: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, createErr := tx.CreateBucketIfNotExists(operationBucket)
		return createErr
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize operation store: %w", err)
	}
	return db, nil
}

func (s *BoltOperationStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *BoltOperationStore) CreatePending(
	operationID string,
	fingerprint string,
	action Action,
) (record OperationRecord, created bool, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationBucket)
		encoded := bucket.Get([]byte(operationID))
		if encoded != nil {
			if err := json.Unmarshal(encoded, &record); err != nil {
				return fmt.Errorf("decode operation record: %w", err)
			}
			if record.Fingerprint != fingerprint {
				return ErrOperationIDConflict
			}
			return nil
		}
		record = OperationRecord{
			Fingerprint: fingerprint, Action: action, State: OperationPending, CreatedAt: s.now(),
		}
		created = true
		return putOperationRecord(bucket, operationID, record)
	})
	return record, created, err
}

func (s *BoltOperationStore) Complete(
	operationID string,
	fingerprint string,
	result Result,
	executionError error,
) error {
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationBucket)
		encoded := bucket.Get([]byte(operationID))
		if encoded == nil {
			return ErrOperationIDConflict
		}
		var record OperationRecord
		if err := json.Unmarshal(encoded, &record); err != nil {
			return fmt.Errorf("decode operation record: %w", err)
		}
		if record.Fingerprint != fingerprint || record.State != OperationPending {
			return ErrOperationIDConflict
		}
		record.State = OperationCompleted
		record.Result = result
		record.CompletedAt = s.now()
		if executionError != nil {
			record.Error = executionError.Error()
		}
		return putOperationRecord(bucket, operationID, record)
	})
	if err != nil {
		return err
	}
	return s.maintainIfDue()
}

func (s *BoltOperationStore) Lookup(
	operationID string,
) (record OperationRecord, found bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		encoded := tx.Bucket(operationBucket).Get([]byte(operationID))
		if encoded == nil {
			return nil
		}
		found = true
		return json.Unmarshal(encoded, &record)
	})
	return record, found, err
}

func putOperationRecord(bucket *bolt.Bucket, operationID string, record OperationRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode operation record: %w", err)
	}
	return bucket.Put([]byte(operationID), encoded)
}

func (s *BoltOperationStore) maintainIfDue() error {
	now := s.now()
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if now.Sub(s.lastMaintenance) < operationMaintenanceEvery {
		return nil
	}
	_, err := s.maintain(now)
	if err == nil {
		s.lastMaintenance = now
	}
	return err
}

func (s *BoltOperationStore) maintain(now time.Time) (changed bool, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationBucket)
		cursor := bucket.Cursor()
		for key, encoded := cursor.First(); key != nil; key, encoded = cursor.Next() {
			var record OperationRecord
			if err := json.Unmarshal(encoded, &record); err != nil {
				return fmt.Errorf("decode operation record %q: %w", key, err)
			}
			if record.State != OperationCompleted {
				continue
			}
			legacyCapture := record.Action == "" && strings.HasPrefix(string(key), "pane-")
			expiredCapture := record.Action == ActionCapture &&
				(record.CompletedAt.IsZero() || now.Sub(record.CompletedAt) >= operationPayloadRetention)
			if legacyCapture || expiredCapture {
				if err := cursor.Delete(); err != nil {
					return err
				}
				changed = true
				continue
			}
			if record.Action != ActionCapture && len(record.Result.Pane) > 0 &&
				!record.CompletedAt.IsZero() &&
				now.Sub(record.CompletedAt) >= operationPayloadRetention {
				record.Result.Pane = nil
				if err := putOperationRecord(bucket, string(key), record); err != nil {
					return err
				}
				changed = true
			}
		}
		return nil
	})
	return changed, err
}

func compactOperationDB(path string) error {
	temporary := path + ".compact"
	if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale operation store compaction: %w", err)
	}
	source, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return fmt.Errorf("open operation store for compaction: %w", err)
	}
	destination, err := bolt.Open(temporary, 0o600, nil)
	if err != nil {
		_ = source.Close()
		return fmt.Errorf("create compact operation store: %w", err)
	}
	copyErr := source.View(func(sourceTx *bolt.Tx) error {
		return destination.Update(func(destinationTx *bolt.Tx) error {
			destinationBucket, err := destinationTx.CreateBucket(operationBucket)
			if err != nil {
				return err
			}
			sourceBucket := sourceTx.Bucket(operationBucket)
			return sourceBucket.ForEach(func(key, value []byte) error {
				return destinationBucket.Put(key, value)
			})
		})
	})
	closeDestinationErr := destination.Close()
	closeSourceErr := source.Close()
	if copyErr != nil || closeDestinationErr != nil || closeSourceErr != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("compact operation store: %w", errors.Join(
			copyErr, closeDestinationErr, closeSourceErr,
		))
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace compact operation store: %w", err)
	}
	return nil
}
