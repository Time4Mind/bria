package artifactproduction

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	retryVersion    = byte(1)
	retryPayloadLen = 29
	retryTagLen     = 16
	retryWireLen    = retryPayloadLen + retryTagLen
	maxRetryRecord  = int64(8 << 10)
)

type retryRecord struct {
	Version    int    `json:"version"`
	FinalID    string `json:"final_id"`
	Digest     string `json:"digest"`
	ExpiresAt  int64  `json:"expires_at"`
	Generation uint32 `json:"generation"`
	State      string `json:"state"`
	ClaimFence string `json:"claim_fence,omitempty"`
}

const retryIssued = "issued"
const retryClaimed = "claimed"
const retryResolved = "resolved"

type retryStore struct {
	directory string
	key       []byte
	ttl       time.Duration
	now       func() time.Time
	mu        sync.Mutex
}

func (store *retryStore) ensure(ctx context.Context, finalID string) (RetryDescriptor, error) {
	if err := store.validate(ctx); err != nil || !validFinalID(finalID) {
		if err != nil {
			return RetryDescriptor{}, err
		}
		return RetryDescriptor{}, ErrInvalidRetry
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	digest := sha256.Sum256([]byte(finalID))
	shortDigest := digest[:16]
	path := store.recordPath(shortDigest)
	now := store.now().UTC()
	record, found, err := loadRetryRecord(path)
	if err != nil {
		return RetryDescriptor{}, err
	}
	if found && (record.FinalID != finalID || record.Digest != hex.EncodeToString(digest[:]) || !validRetryRecord(record)) {
		return RetryDescriptor{}, ErrInvalidRetry
	}
	if found && record.State == retryClaimed {
		return RetryDescriptor{}, ErrRetryRecoveryRequired
	}
	if !found || record.ExpiresAt <= now.Unix() {
		expires := now.Add(store.ttl).Truncate(time.Second)
		if !expires.After(now) {
			return RetryDescriptor{}, ErrInvalidConfiguration
		}
		generation := uint32(1)
		if found {
			if record.Generation == ^uint32(0) {
				return RetryDescriptor{}, ErrInvalidRetry
			}
			generation = record.Generation + 1
		}
		record = retryRecord{Version: 1, FinalID: finalID, Digest: hex.EncodeToString(digest[:]), ExpiresAt: expires.Unix(), Generation: generation, State: retryIssued, ClaimFence: ""}
		if err := writeRetryRecord(path, record); err != nil {
			return RetryDescriptor{}, err
		}
	}
	expiresAt := time.Unix(record.ExpiresAt, 0).UTC()
	return RetryDescriptor{Token: store.encode(shortDigest, expiresAt, record.Generation), ExpiresAt: expiresAt}, nil
}

func (store *retryStore) resolve(ctx context.Context, token string, fence func(string) (string, error)) (retryRecord, error) {
	if err := store.validate(ctx); err != nil {
		return retryRecord{}, err
	}
	if fence == nil {
		return retryRecord{}, ErrInvalidRetry
	}
	shortDigest, expiresAt, generation, err := store.decode(token)
	if err != nil {
		return retryRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found, err := loadRetryRecord(store.recordPath(shortDigest))
	if err != nil || !found {
		if err != nil {
			return retryRecord{}, err
		}
		return retryRecord{}, ErrInvalidRetry
	}
	digest := sha256.Sum256([]byte(record.FinalID))
	if !validRetryRecord(record) || record.Digest != hex.EncodeToString(digest[:]) ||
		!hmac.Equal(shortDigest, digest[:16]) || record.ExpiresAt != expiresAt.Unix() ||
		record.Generation != generation || record.State != retryIssued {
		return retryRecord{}, ErrInvalidRetry
	}
	claimFence, err := fence(record.FinalID)
	if err != nil || len(claimFence) != sha256.Size*2 {
		if err != nil {
			return retryRecord{}, err
		}
		return retryRecord{}, ErrInvalidRetry
	}
	record.State = retryClaimed
	record.ClaimFence = claimFence
	if err := writeRetryRecord(store.recordPath(shortDigest), record); err != nil {
		return retryRecord{}, err
	}
	return record, nil
}

func (store *retryStore) recoverable(ctx context.Context) ([]retryRecord, error) {
	if err := store.validate(ctx); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entries, err := os.ReadDir(store.directory)
	if err != nil {
		return nil, err
	}
	if len(entries) > 10_001 {
		return nil, ErrInvalidRetry
	}
	records := make([]retryRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, found, err := loadRetryRecord(filepath.Join(store.directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		if found && (record.State == retryClaimed || record.State == retryIssued || record.State == retryResolved) {
			records = append(records, record)
		}
	}
	return records, nil
}

func (store *retryStore) rotateClaimed(ctx context.Context, expected retryRecord) (RetryDescriptor, bool, error) {
	current, rotated, err := store.transitionClaimed(ctx, expected, false)
	if err != nil || !rotated {
		return RetryDescriptor{}, rotated, err
	}
	digest, _ := hex.DecodeString(current.Digest)
	expires := time.Unix(current.ExpiresAt, 0).UTC()
	return RetryDescriptor{Token: store.encode(digest[:16], expires, current.Generation), ExpiresAt: expires}, true, nil
}

func (store *retryStore) resolveClaimed(ctx context.Context, expected retryRecord) (bool, error) {
	_, resolved, err := store.transitionClaimed(ctx, expected, true)
	return resolved, err
}

func (store *retryStore) transitionClaimed(ctx context.Context, expected retryRecord, resolve bool) (retryRecord, bool, error) {
	if err := store.validate(ctx); err != nil {
		return retryRecord{}, false, err
	}
	digest, err := hex.DecodeString(expected.Digest)
	if err != nil || len(digest) != sha256.Size {
		return retryRecord{}, false, ErrInvalidRetry
	}
	path := store.recordPath(digest[:16])
	store.mu.Lock()
	defer store.mu.Unlock()
	current, found, err := loadRetryRecord(path)
	if err != nil || !found {
		return retryRecord{}, false, err
	}
	if current != expected || current.State != retryClaimed {
		return retryRecord{}, false, nil
	}
	if resolve {
		current.State, current.ClaimFence = retryResolved, ""
	} else {
		now := store.now().UTC()
		expires := now.Add(store.ttl).Truncate(time.Second)
		if current.Generation == ^uint32(0) || !expires.After(now) {
			return retryRecord{}, false, ErrInvalidConfiguration
		}
		current.Generation++
		current.State, current.ClaimFence, current.ExpiresAt = retryIssued, "", expires.Unix()
	}
	if err := writeRetryRecord(path, current); err != nil {
		return retryRecord{}, false, err
	}
	return current, true, nil
}

func (store *retryStore) encode(digest []byte, expiresAt time.Time, generation uint32) string {
	payload := make([]byte, retryPayloadLen)
	payload[0] = retryVersion
	copy(payload[1:17], digest)
	binary.BigEndian.PutUint64(payload[17:25], uint64(expiresAt.Unix()))
	binary.BigEndian.PutUint32(payload[25:29], generation)
	wire := append(payload, retryTag(store.key, payload)...)
	return base64.RawURLEncoding.EncodeToString(wire)
}

func (store *retryStore) decode(token string) ([]byte, time.Time, uint32, error) {
	wire, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(wire) != retryWireLen || wire[0] != retryVersion ||
		!hmac.Equal(wire[retryPayloadLen:], retryTag(store.key, wire[:retryPayloadLen])) {
		return nil, time.Time{}, 0, ErrInvalidRetry
	}
	expiresUnix := binary.BigEndian.Uint64(wire[17:25])
	if expiresUnix > uint64(^uint64(0)>>1) {
		return nil, time.Time{}, 0, ErrInvalidRetry
	}
	expires := time.Unix(int64(expiresUnix), 0).UTC()
	if !expires.After(store.now().UTC()) {
		return nil, time.Time{}, 0, ErrRetryExpired
	}
	generation := binary.BigEndian.Uint32(wire[25:29])
	if generation == 0 {
		return nil, time.Time{}, 0, ErrInvalidRetry
	}
	return append([]byte(nil), wire[1:17]...), expires, generation, nil
}

func (store *retryStore) recordPath(digest []byte) string {
	return filepath.Join(store.directory, hex.EncodeToString(digest)+".json")
}

func (store *retryStore) validate(ctx context.Context) error {
	if store == nil || store.directory == "" || len(store.key) < 32 || store.ttl <= 0 || store.now == nil {
		return ErrInvalidConfiguration
	}
	if ctx == nil {
		return ErrInvalidRetry
	}
	return ctx.Err()
}

func retryTag(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)[:retryTagLen]
}

func validFinalID(value string) bool {
	return value != "" && len(value) <= 1024 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validRetryRecord(record retryRecord) bool {
	if record.Version != 1 || !validFinalID(record.FinalID) || len(record.Digest) != sha256.Size*2 ||
		record.ExpiresAt <= 0 || record.Generation == 0 || (record.State != retryIssued && record.State != retryClaimed && record.State != retryResolved) {
		return false
	}
	if ((record.State == retryIssued || record.State == retryResolved) && record.ClaimFence != "") ||
		(record.State == retryClaimed && len(record.ClaimFence) != sha256.Size*2) {
		return false
	}
	_, err := hex.DecodeString(record.Digest)
	return err == nil
}

func loadRetryRecord(path string) (retryRecord, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return retryRecord{}, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxRetryRecord {
		return retryRecord{}, false, ErrInvalidRetry
	}
	handle, err := os.Open(path)
	if err != nil {
		return retryRecord{}, false, ErrInvalidRetry
	}
	defer handle.Close()
	data, err := io.ReadAll(io.LimitReader(handle, maxRetryRecord+1))
	if err != nil || int64(len(data)) != info.Size() {
		return retryRecord{}, false, ErrInvalidRetry
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record retryRecord
	if err := decoder.Decode(&record); err != nil {
		return retryRecord{}, false, ErrInvalidRetry
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return retryRecord{}, false, ErrInvalidRetry
	}
	if !validRetryRecord(record) {
		return retryRecord{}, false, ErrInvalidRetry
	}
	return record, true, nil
}

func writeRetryRecord(path string, record retryRecord) (returnErr error) {
	encoded, err := json.Marshal(record)
	if err != nil || int64(len(encoded)) > maxRetryRecord {
		return ErrInvalidRetry
	}
	return writePrivateAtomic(path, ".retry-", encoded, func() error {
		persisted, found, err := loadRetryRecord(path)
		if err != nil || !found || persisted != record {
			return ErrInvalidRetry
		}
		return nil
	})
}
