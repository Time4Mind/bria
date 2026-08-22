package runtimehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/processlog"
	bolt "go.etcd.io/bbolt"
)

var operationBucket = []byte("runtime_operations_v1")

const (
	operationPayloadRetention   = 15 * time.Minute
	operationRecordRetention    = 30 * 24 * time.Hour
	operationMaintenanceEvery   = time.Hour
	operationMaintenanceBatch   = 1000
	operationMaintenanceMaxScan = 10 * operationMaintenanceBatch
	operationCompactionMinSize  = 64 << 20
)

type BoltOperationStore struct {
	db                *bolt.DB
	path              string
	now               func() time.Time
	maintenanceMu     sync.Mutex
	maintenanceCursor []byte
}

type operationMaintenanceReport struct {
	Scanned          int
	Deleted          int
	PendingDeleted   int
	ProtectedPending int
	PayloadTrimmed   int
	Remaining        int
	FileBytes        int64
	Duration         time.Duration
}

func OpenBoltOperationStore(path string) (*BoltOperationStore, error) {
	return openBoltOperationStore(path, time.Now)
}

func openBoltOperationStore(path string, now func() time.Time) (*BoltOperationStore, error) {
	return openBoltOperationStoreWithCompaction(path, now, operationCompactionMinSize)
}

func openBoltOperationStoreWithCompaction(
	path string,
	now func() time.Time,
	compactionMinSize int64,
) (*BoltOperationStore, error) {
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
	store := &BoltOperationStore{db: db, path: path, now: now}
	_, err = store.maintain(context.Background(), now())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	compact, err := shouldCompactOperationDB(db, path, compactionMinSize)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !compact {
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
	return nil
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

// RunMaintenance owns one bounded hourly sweep. Records are streamed through
// short Bolt transactions and are never accumulated in memory.
func (s *BoltOperationStore) RunMaintenance(
	ctx context.Context,
	activeOperations func() map[string]struct{},
) {
	ticker := time.NewTicker(operationMaintenanceEvery)
	defer ticker.Stop()
	s.runMaintenance(ctx, ticker.C, activeOperations)
}

func (s *BoltOperationStore) runMaintenance(
	ctx context.Context,
	ticks <-chan time.Time,
	activeOperations func() map[string]struct{},
) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			var active map[string]struct{}
			if activeOperations != nil {
				active = activeOperations()
			}
			report, err := s.maintainProtected(ctx, s.now(), active)
			if err != nil {
				if ctx.Err() == nil {
					processlog.Failuref(
						processlog.Service, processlog.FailureIO,
						"bria runtime_operations: outcome=maintenance_failed",
					)
				}
				continue
			}
			processlog.Failuref(
				processlog.Service, processlog.FailureNone,
				"bria runtime_operations: outcome=maintained scanned=%d deleted=%d pending_deleted=%d protected_pending=%d payload_trimmed=%d remaining=%d file_bytes=%d duration_ms=%d",
				report.Scanned, report.Deleted, report.PendingDeleted,
				report.ProtectedPending, report.PayloadTrimmed, report.Remaining, report.FileBytes,
				report.Duration.Milliseconds(),
			)
		}
	}
}

func (s *BoltOperationStore) maintain(
	ctx context.Context,
	now time.Time,
) (operationMaintenanceReport, error) {
	return s.maintainProtected(ctx, now, nil)
}

func (s *BoltOperationStore) maintainProtected(
	ctx context.Context,
	now time.Time,
	active map[string]struct{},
) (operationMaintenanceReport, error) {
	startedAt := time.Now()
	report := operationMaintenanceReport{}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	start := append([]byte(nil), s.maintenanceCursor...)
	for {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		batch, next, err := s.maintainBatch(now, start, active)
		report.Scanned += batch.Scanned
		report.Deleted += batch.Deleted
		report.PendingDeleted += batch.PendingDeleted
		report.ProtectedPending += batch.ProtectedPending
		report.PayloadTrimmed += batch.PayloadTrimmed
		if err != nil {
			return report, err
		}
		if next == nil || report.Scanned >= operationMaintenanceMaxScan {
			s.maintenanceCursor = append(s.maintenanceCursor[:0], next...)
			break
		}
		start = next
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		report.Remaining = tx.Bucket(operationBucket).Stats().KeyN
		return nil
	}); err != nil {
		return report, err
	}
	if info, err := os.Stat(s.path); err == nil {
		report.FileBytes = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	report.Duration = time.Since(startedAt)
	return report, nil
}

func (s *BoltOperationStore) maintainBatch(
	now time.Time,
	start []byte,
	active map[string]struct{},
) (operationMaintenanceReport, []byte, error) {
	report := operationMaintenanceReport{}
	var next []byte
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationBucket)
		cursor := bucket.Cursor()
		key, encoded := cursor.First()
		if start != nil {
			key, encoded = cursor.Seek(start)
		}
		for key != nil && report.Scanned < operationMaintenanceBatch {
			report.Scanned++
			var record OperationRecord
			if err := json.Unmarshal(encoded, &record); err != nil {
				return fmt.Errorf("decode operation record %q: %w", key, err)
			}
			_, protected := active[string(key)]
			deleteRecord, pending, protectedPending, changed, payloadTrimmed := maintainOperationRecord(
				string(key), &record, now, protected,
			)
			if deleteRecord {
				if err := cursor.Delete(); err != nil {
					return err
				}
				report.Deleted++
				if pending {
					report.PendingDeleted++
				}
			} else {
				if protectedPending {
					report.ProtectedPending++
				}
				if changed {
					if err := putOperationRecord(bucket, string(key), record); err != nil {
						return err
					}
					if payloadTrimmed {
						report.PayloadTrimmed++
					}
				}
			}
			key, encoded = cursor.Next()
		}
		if key != nil {
			next = append([]byte(nil), key...)
		}
		return nil
	})
	return report, next, err
}

func maintainOperationRecord(
	operationID string,
	record *OperationRecord,
	now time.Time,
	protected bool,
) (deleteRecord bool, pending bool, protectedPending bool, changed bool, payloadTrimmed bool) {
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
		record.CreatedAt = createdAt
		changed = true
	}
	if !createdAt.After(now) && now.Sub(createdAt) >= operationRecordRetention {
		if record.State == OperationPending && protected {
			return false, false, true, changed, false
		}
		return true, record.State == OperationPending, false, false, false
	}
	if record.State != OperationCompleted {
		return false, false, false, changed, false
	}
	legacyCapture := record.Action == "" && strings.HasPrefix(operationID, "pane-")
	expiredCapture := record.Action == ActionCapture &&
		(record.CompletedAt.IsZero() ||
			(!record.CompletedAt.After(now) && now.Sub(record.CompletedAt) >= operationPayloadRetention))
	if legacyCapture || expiredCapture {
		return true, false, false, false, false
	}
	if record.Action != ActionCapture && completedPayloadPresent(record) &&
		!record.CompletedAt.IsZero() && !record.CompletedAt.After(now) &&
		now.Sub(record.CompletedAt) >= operationPayloadRetention {
		record.Result.Pane = nil
		record.Result.ResolvedText = ""
		record.Result.GeneratedName = ""
		record.Result.Detail = ""
		changed = true
		payloadTrimmed = true
	}
	return false, false, false, changed, payloadTrimmed
}

func completedPayloadPresent(record *OperationRecord) bool {
	return len(record.Result.Pane) > 0 || record.Result.ResolvedText != "" ||
		record.Result.GeneratedName != "" || record.Result.Detail != ""
}

func shouldCompactOperationDB(db *bolt.DB, path string, minSize int64) (bool, error) {
	if minSize <= 0 {
		minSize = operationCompactionMinSize
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("inspect operation store for compaction: %w", err)
	}
	if info.Size() < minSize {
		return false, nil
	}
	stats := db.Stats()
	freeBytes := int64(stats.FreePageN+stats.PendingPageN) * int64(db.Info().PageSize)
	return freeBytes >= info.Size()/2, nil
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
