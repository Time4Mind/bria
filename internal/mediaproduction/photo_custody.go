package mediaproduction

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"bria/internal/mediaflow"
)

const (
	photoRecordVersion = 1
	photoPayloadName   = "photo"
	photoMetadataName  = "custody.json"
	maxPhotoRecordSize = int64(16 << 10)
)

type PhotoState string

const (
	PhotoPrepared  PhotoState = "prepared"
	PhotoAccepted  PhotoState = "accepted"
	photoCompleted PhotoState = "completed"
	PhotoReleased  PhotoState = "released"
)

type AttachmentReceipt struct {
	Reference         string
	ProviderSessionID string
	MessageID         string
}
type photoRecord struct {
	Version           int        `json:"version"`
	ID                string     `json:"id"`
	FileID            string     `json:"file_id"`
	FileUniqueID      string     `json:"file_unique_id"`
	MIMEType          string     `json:"mime_type"`
	Width             int        `json:"width"`
	Height            int        `json:"height"`
	Size              int64      `json:"size"`
	SHA256            string     `json:"sha256"`
	State             PhotoState `json:"state"`
	ProviderSessionID string     `json:"provider_session_id,omitempty"`
	MessageID         string     `json:"message_id,omitempty"`
}

var photoLocks sync.Map

type PhotoCustody struct {
	directory string
	maxBytes  int64
}

func OpenPhotoCustody(directory string, maxBytes int64) (*PhotoCustody, error) {
	if !validBound(maxBytes) {
		return nil, ErrInvalidConfiguration
	}
	resolved, err := prepareDirectory(directory)
	if err != nil {
		return nil, fmt.Errorf("prepare photo custody directory: %w", err)
	}
	store := &PhotoCustody{directory: resolved, maxBytes: maxBytes}
	if err := store.reconcile(context.Background()); err != nil {
		return nil, fmt.Errorf("reconcile photo custody: %w", err)
	}
	return store, nil
}
func (store *PhotoCustody) AttachPhoto(ctx context.Context, attachment mediaflow.PhotoAttachment) (string, error) {
	if err := store.validateContext(ctx); err != nil {
		return "", err
	}
	if !validAttachment(attachment, store.maxBytes) {
		return "", ErrInvalidPhoto
	}
	lock := lockPhotoDirectory(store.directory)
	lock.Lock()
	defer lock.Unlock()
	release, err := acquirePhotoFileLock(filepath.Join(store.directory, ".custody.lock"))
	if err != nil {
		return "", err
	}
	defer release()
	id, err := randomPhotoID()
	if err != nil {
		return "", fmt.Errorf("create photo custody identity: %w", err)
	}
	temporary, err := os.MkdirTemp(store.directory, ".photo-")
	if err != nil {
		return "", fmt.Errorf("create photo custody temporary directory: %w", err)
	}
	promoted := false
	defer func() {
		if !promoted {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0o700); err != nil {
		return "", err
	}
	if err := writeNewFile(filepath.Join(temporary, photoPayloadName), attachment.Content); err != nil {
		return "", fmt.Errorf("persist photo payload: %w", err)
	}
	digest := sha256.Sum256(attachment.Content)
	record := photoRecord{
		Version: photoRecordVersion, ID: id, FileID: attachment.FileID,
		FileUniqueID: attachment.FileUniqueID, MIMEType: attachment.MIMEType,
		Width: attachment.Width, Height: attachment.Height, Size: int64(len(attachment.Content)),
		SHA256: hex.EncodeToString(digest[:]), State: PhotoPrepared,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	if err := writeNewFile(filepath.Join(temporary, photoMetadataName), encoded); err != nil {
		return "", fmt.Errorf("persist photo custody metadata: %w", err)
	}
	if err := syncDirectory(temporary); err != nil {
		return "", fmt.Errorf("sync photo custody temporary directory: %w", err)
	}
	finalDirectory := filepath.Join(store.directory, id)
	if err := os.Rename(temporary, finalDirectory); err != nil {
		return "", fmt.Errorf("promote photo custody: %w", err)
	}
	promoted = true
	if err := syncDirectory(store.directory); err != nil {
		return "", fmt.Errorf("sync photo custody directory: %w", err)
	}
	reference := id
	if err := store.verifyUnlocked(reference, mediaflow.PhotoDigest{Size: record.Size, SHA256: record.SHA256}); err != nil {
		return "", fmt.Errorf("verify promoted photo custody: %w", err)
	}
	return reference, nil
}
func (store *PhotoCustody) VerifyPhoto(ctx context.Context, reference string, digest mediaflow.PhotoDigest) error {
	if err := store.validateContext(ctx); err != nil {
		return err
	}
	lock := lockPhotoDirectory(store.directory)
	lock.Lock()
	defer lock.Unlock()
	release, err := acquirePhotoFileLock(filepath.Join(store.directory, ".custody.lock"))
	if err != nil {
		return err
	}
	defer release()
	return store.verifyUnlocked(reference, digest)
}
func (store *PhotoCustody) verifyUnlocked(reference string, digest mediaflow.PhotoDigest) error {
	record, _, err := store.load(reference)
	if err != nil {
		return err
	}
	if record.State != PhotoPrepared && record.State != PhotoAccepted {
		return ErrInvalidTransition
	}
	if digest.Size <= 0 || digest.SHA256 == "" || record.Size != digest.Size || record.SHA256 != digest.SHA256 {
		return ErrPhotoCorrupt
	}
	content, err := readRegularBounded(filepath.Join(store.directory, reference, photoPayloadName), store.maxBytes)
	if err != nil {
		return errors.Join(ErrPhotoCorrupt, err)
	}
	hash := sha256.Sum256(content)
	if int64(len(content)) != digest.Size || !strings.EqualFold(hex.EncodeToString(hash[:]), digest.SHA256) {
		return ErrPhotoCorrupt
	}
	return nil
}
func (store *PhotoCustody) MarkAccepted(ctx context.Context, receipt AttachmentReceipt) error {
	if err := store.validateContext(ctx); err != nil {
		return err
	}
	if !validReceipt(receipt) {
		return ErrInvalidPhoto
	}
	lock := lockPhotoDirectory(store.directory)
	lock.Lock()
	defer lock.Unlock()
	release, err := acquirePhotoFileLock(filepath.Join(store.directory, ".custody.lock"))
	if err != nil {
		return err
	}
	defer release()
	record, directory, err := store.load(receipt.Reference)
	if err != nil {
		return err
	}
	switch record.State {
	case PhotoPrepared:
		if err := store.verifyUnlocked(receipt.Reference, mediaflow.PhotoDigest{Size: record.Size, SHA256: record.SHA256}); err != nil {
			return err
		}
		record.State = PhotoAccepted
		record.ProviderSessionID = receipt.ProviderSessionID
		record.MessageID = receipt.MessageID
		return writeRecord(directory, record)
	case PhotoAccepted, photoCompleted, PhotoReleased:
		if record.ProviderSessionID != receipt.ProviderSessionID || record.MessageID != receipt.MessageID {
			return ErrReceiptMismatch
		}
		return nil
	default:
		return ErrInvalidTransition
	}
}
func (store *PhotoCustody) MarkCompleted(ctx context.Context, receipt AttachmentReceipt) error {
	if err := store.validateContext(ctx); err != nil {
		return err
	}
	if !validReceipt(receipt) {
		return ErrInvalidPhoto
	}
	lock := lockPhotoDirectory(store.directory)
	lock.Lock()
	defer lock.Unlock()
	release, err := acquirePhotoFileLock(filepath.Join(store.directory, ".custody.lock"))
	if err != nil {
		return err
	}
	defer release()
	record, directory, err := store.load(receipt.Reference)
	if err != nil {
		return err
	}
	if record.State == PhotoPrepared {
		return ErrInvalidTransition
	}
	if record.ProviderSessionID != receipt.ProviderSessionID || record.MessageID != receipt.MessageID {
		return ErrReceiptMismatch
	}
	if record.State == PhotoReleased {
		return nil
	}
	if record.State == PhotoAccepted {
		record.State = photoCompleted
		if err := writeRecord(directory, record); err != nil {
			return err
		}
	}
	return store.releaseCompleted(directory, record)
}
func (store *PhotoCustody) Status(ctx context.Context, reference string) (PhotoState, error) {
	if err := store.validateContext(ctx); err != nil {
		return "", err
	}
	lock := lockPhotoDirectory(store.directory)
	lock.Lock()
	defer lock.Unlock()
	release, err := acquirePhotoFileLock(filepath.Join(store.directory, ".custody.lock"))
	if err != nil {
		return "", err
	}
	defer release()
	record, _, err := store.load(reference)
	if err != nil {
		return "", err
	}
	return record.State, nil
}
func (store *PhotoCustody) reconcile(ctx context.Context) error {
	if err := store.validateContext(ctx); err != nil {
		return err
	}
	lock := lockPhotoDirectory(store.directory)
	lock.Lock()
	defer lock.Unlock()
	release, err := acquirePhotoFileLock(filepath.Join(store.directory, ".custody.lock"))
	if err != nil {
		return err
	}
	defer release()
	entries, err := os.ReadDir(store.directory)
	if err != nil {
		return err
	}
	removedTemporary := false
	for _, entry := range entries {
		if entry.Name() == ".custody.lock" {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".photo-") {
			candidate := filepath.Join(store.directory, entry.Name())
			info, err := os.Lstat(candidate)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return ErrPhotoCorrupt
			}
			if err := os.RemoveAll(candidate); err != nil {
				return fmt.Errorf("remove stale photo custody temporary: %w", err)
			}
			removedTemporary = true
			continue
		}
		if !entry.IsDir() || !validPhotoID(entry.Name()) {
			return ErrPhotoCorrupt
		}
		directory := filepath.Join(store.directory, entry.Name())
		record, _, err := store.load(entry.Name())
		if err != nil {
			return err
		}
		if record.State == photoCompleted {
			if err := store.releaseCompleted(directory, record); err != nil {
				return err
			}
		}
	}
	if removedTemporary {
		return syncDirectory(store.directory)
	}
	return nil
}
func (store *PhotoCustody) releaseCompleted(directory string, record photoRecord) error {
	if record.State != photoCompleted {
		return ErrInvalidTransition
	}
	payload := filepath.Join(directory, photoPayloadName)
	if err := os.Remove(payload); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove completed photo payload: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync released photo directory: %w", err)
	}
	record.State = PhotoReleased
	return writeRecord(directory, record)
}
func (store *PhotoCustody) load(reference string) (photoRecord, string, error) {
	directory, err := store.referenceDirectory(reference)
	if err != nil {
		return photoRecord{}, "", err
	}
	encoded, err := readRegularBounded(filepath.Join(directory, photoMetadataName), maxPhotoRecordSize)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return photoRecord{}, "", ErrUnknownPhoto
		}
		return photoRecord{}, "", errors.Join(ErrPhotoCorrupt, err)
	}
	var record photoRecord
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || decoder.More() || !validRecord(record, filepath.Base(directory), store.maxBytes) {
		return photoRecord{}, "", ErrPhotoCorrupt
	}
	return record, directory, nil
}
func (store *PhotoCustody) referenceDirectory(reference string) (string, error) {
	if store == nil || store.directory == "" || !validPhotoID(reference) {
		return "", ErrUnknownPhoto
	}
	directory := filepath.Join(store.directory, reference)
	info, err := os.Lstat(directory)
	if err != nil {
		return "", ErrUnknownPhoto
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", ErrPhotoCorrupt
	}
	return directory, nil
}
func (store *PhotoCustody) validateContext(ctx context.Context) error {
	if store == nil || store.directory == "" || !validBound(store.maxBytes) {
		return ErrInvalidConfiguration
	}
	if ctx == nil {
		return ErrInvalidPhoto
	}
	return ctx.Err()
}
func validAttachment(attachment mediaflow.PhotoAttachment, maxBytes int64) bool {
	return validOpaque(attachment.FileID) && validOptionalOpaque(attachment.FileUniqueID) &&
		(attachment.MIMEType == "image/jpeg" || attachment.MIMEType == "image/png" || attachment.MIMEType == "image/webp") &&
		attachment.Width >= 0 && attachment.Height >= 0 && len(attachment.Content) > 0 && int64(len(attachment.Content)) <= maxBytes
}
func validReceipt(receipt AttachmentReceipt) bool {
	return receipt.Reference != "" && validOpaque(receipt.ProviderSessionID) && validOpaque(receipt.MessageID)
}
func validRecord(record photoRecord, id string, maxBytes int64) bool {
	if record.Version != photoRecordVersion || record.ID != id || !validPhotoID(record.ID) ||
		!validOpaque(record.FileID) || !validOptionalOpaque(record.FileUniqueID) ||
		(record.MIMEType != "image/jpeg" && record.MIMEType != "image/png" && record.MIMEType != "image/webp") ||
		record.Width < 0 || record.Height < 0 || record.Size <= 0 || record.Size > maxBytes || len(record.SHA256) != sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(record.SHA256); err != nil {
		return false
	}
	if record.State == PhotoPrepared {
		return record.ProviderSessionID == "" && record.MessageID == ""
	}
	return (record.State == PhotoAccepted || record.State == photoCompleted || record.State == PhotoReleased) && validOpaque(record.ProviderSessionID) && validOpaque(record.MessageID)
}
func validOpaque(value string) bool {
	return value != "" && len(value) <= 1024 && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && strings.TrimSpace(value) == value
}

func validOptionalOpaque(value string) bool { return value == "" || validOpaque(value) }
func randomPhotoID() (string, error) {
	raw := make([]byte, 16)
	_, err := io.ReadFull(rand.Reader, raw)
	return hex.EncodeToString(raw), err
}
func validPhotoID(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 32 && err == nil && strings.ToLower(value) == value
}
func writeNewFile(path string, content []byte) (returnErr error) {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := handle.Close(); returnErr == nil && closeErr != nil {
			returnErr = closeErr
		}
	}()
	if _, err := handle.Write(content); err != nil {
		return err
	}
	return handle.Sync()
}
func writeRecord(directory string, record photoRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if int64(len(encoded)) > maxPhotoRecordSize {
		return ErrInvalidPhoto
	}
	temporary, err := os.CreateTemp(directory, ".custody-")
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
	if err := os.Rename(temporaryPath, filepath.Join(directory, photoMetadataName)); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	persisted, err := readRegularBounded(filepath.Join(directory, photoMetadataName), maxPhotoRecordSize)
	if err != nil || !bytes.Equal(persisted, encoded) {
		return ErrPhotoCorrupt
	}
	return nil
}
func readRegularBounded(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxBytes {
		return nil, ErrPhotoCorrupt
	}
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Size() != info.Size() {
		return nil, ErrPhotoCorrupt
	}
	content, err := io.ReadAll(io.LimitReader(handle, maxBytes+1))
	if err != nil {
		return nil, err
	}
	after, err := handle.Stat()
	if err != nil || !os.SameFile(opened, after) || int64(len(content)) > maxBytes || int64(len(content)) != after.Size() {
		return nil, ErrPhotoCorrupt
	}
	return content, nil
}
func lockPhotoDirectory(path string) *sync.Mutex {
	value, _ := photoLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (store *PhotoCustody) ResolveAttachment(ctx context.Context, reference string) (string, error) {
	if err := store.validateContext(ctx); err != nil {
		if ctx != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrPhotoUnavailable
	}
	if !validPhotoID(reference) {
		return "", ErrPhotoUnavailable
	}
	lock := lockPhotoDirectory(store.directory)
	lock.Lock()
	defer lock.Unlock()
	release, err := acquirePhotoFileLock(filepath.Join(store.directory, ".custody.lock"))
	if err != nil {
		return "", ErrPhotoUnavailable
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	record, directory, err := store.load(reference)
	if err != nil || record.State != PhotoPrepared && record.State != PhotoAccepted {
		return "", ErrPhotoUnavailable
	}
	if err := store.resolvePayload(ctx, directory, record); err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return "", err
		}
		return "", ErrPhotoUnavailable
	}
	return filepath.Join(directory, photoPayloadName), nil
}

func (store *PhotoCustody) resolvePayload(ctx context.Context, directory string, record photoRecord) error {
	for _, path := range []string{store.directory, directory} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrPhotoCorrupt
		}
	}
	metadata, err := os.Lstat(filepath.Join(directory, photoMetadataName))
	if err != nil || metadata.Mode()&os.ModeSymlink != 0 || !metadata.Mode().IsRegular() {
		return ErrPhotoCorrupt
	}
	path := filepath.Join(directory, photoPayloadName)
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() != record.Size {
		return ErrPhotoCorrupt
	}
	content, err := readRegularBounded(path, store.maxBytes)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err != nil || int64(len(content)) != record.Size {
		return ErrPhotoCorrupt
	}
	hash := sha256.Sum256(content)
	if hex.EncodeToString(hash[:]) != record.SHA256 {
		return ErrPhotoCorrupt
	}
	latest, _, err := store.load(record.ID)
	if err != nil || latest != record {
		return ErrPhotoCorrupt
	}
	return nil
}
