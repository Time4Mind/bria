package runtimehost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var operationBucket = []byte("runtime_operations_v1")

type BoltOperationStore struct {
	db *bolt.DB
}

func OpenBoltOperationStore(path string) (*BoltOperationStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("operation store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create operation store directory: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open operation store: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(operationBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize operation store: %w", err)
	}
	return &BoltOperationStore{db: db}, nil
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
		record = OperationRecord{Fingerprint: fingerprint, State: OperationPending}
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
	return s.db.Update(func(tx *bolt.Tx) error {
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
		if executionError != nil {
			record.Error = executionError.Error()
		}
		return putOperationRecord(bucket, operationID, record)
	})
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
