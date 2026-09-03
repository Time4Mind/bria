package coordinatorstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"bria/internal/coordinatorbundle"
	"bria/internal/coordinatortransfer"
)

var (
	ErrInvalidState     = errors.New("invalid coordinator state candidate")
	ErrSensitiveState   = errors.New("coordinator state contains forbidden secret material")
	ErrRecoveryRequired = errors.New("coordinator state activation requires explicit recovery")
)

const (
	formatVersion    = 1
	maxComponentSize = 32 << 20
	maxMetadataSize  = 1 << 20
)

var componentNames = []string{
	"catalog.json", "routes.json", "settings.json", "sessions.json", "telegram-scope.json", "telegram-ui.json",
	"journals.json", "inputs.json", "outputs.json", "checkpoint.json",
	"callback-key-id.json", "callback-registry.json", "callback-operations.json",
}

type manifest struct {
	Version                        int               `json:"version"`
	SnapshotVersion                int               `json:"snapshot_version"`
	TransferID                     string            `json:"transfer_id"`
	Digest                         string            `json:"digest"`
	Components                     map[string]string `json:"components"`
	CallbackOperationStateIncluded bool              `json:"callback_operation_state_included"`
	CallbackSigningSecretsIncluded bool              `json:"callback_signing_secrets_included"`
}

type pointer struct {
	Version            int    `json:"version"`
	TransferID         string `json:"transfer_id"`
	Digest             string `json:"digest"`
	PreviousTransferID string `json:"previous_transfer_id,omitempty"`
	PreviousDigest     string `json:"previous_digest,omitempty"`
}

type activationMarker struct {
	Version  int      `json:"version"`
	Previous *pointer `json:"previous,omitempty"`
	Next     pointer  `json:"next"`
}

type Store struct {
	mu   sync.Mutex
	root string
}

var _ coordinatortransfer.AtomicStateStore = (*Store)(nil)

func Open(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrInvalidState
	}
	absolute, err := filepath.Abs(root)
	if err != nil || filepath.Dir(absolute) == absolute {
		return nil, ErrInvalidState
	}
	if err := preparePrivateRoot(absolute); err != nil {
		return nil, err
	}
	store := &Store{root: absolute}
	if _, err := os.Lstat(store.markerPath()); err == nil {
		return nil, ErrRecoveryRequired
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Lstat(store.pointerPath()); err == nil {
		if _, _, err := store.readActive(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (store *Store) Root() string {
	if store == nil {
		return ""
	}
	return store.root
}

func (store *Store) Stage(ctx context.Context, transferID string, snapshot coordinatorbundle.Bundle) (coordinatortransfer.SnapshotReceipt, error) {
	if store == nil || ctx == nil || !safeID(transferID) {
		return coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	digest, _ := snapshot.Digest()
	receipt := coordinatortransfer.SnapshotReceipt{TransferID: transferID, Digest: digest}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireNoRecovery(); err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	destination := store.versionPath(digest)
	if _, err := os.Lstat(destination); err == nil {
		_, existing, readErr := store.readVersion(digest)
		if readErr != nil || existing != receipt {
			return coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
		}
		return receipt, nil
	}
	temporary, err := os.MkdirTemp(filepath.Join(store.root, "versions"), ".candidate-")
	if err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	defer os.RemoveAll(temporary)
	components := map[string]any{
		"catalog.json": snapshot.Catalog, "routes.json": snapshot.Routes,
		"settings.json": snapshot.Settings, "sessions.json": snapshot.Sessions, "telegram-scope.json": snapshot.TelegramScope,
		"telegram-ui.json": snapshot.TelegramUI, "journals.json": snapshot.Journals,
		"inputs.json": snapshot.Inputs, "outputs.json": snapshot.Outputs,
		"checkpoint.json": snapshot.Checkpoint, "callback-key-id.json": snapshot.CallbackVerificationKeyID,
		"callback-registry.json":   snapshot.CallbackRegistry,
		"callback-operations.json": snapshot.CallbackOperations,
	}
	hashes := make(map[string]string, len(components))
	for _, name := range componentNames {
		if err := ctx.Err(); err != nil {
			return coordinatortransfer.SnapshotReceipt{}, err
		}
		encoded, err := marshalCanonical(components[name])
		if err != nil || len(encoded) > maxComponentSize {
			return coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
		}
		if sensitive(encoded) {
			return coordinatortransfer.SnapshotReceipt{}, fmt.Errorf("%w: %s", ErrSensitiveState, name)
		}
		if err := writeSynced(filepath.Join(temporary, name), encoded); err != nil {
			return coordinatortransfer.SnapshotReceipt{}, err
		}
		hashes[name] = hashBytes(encoded)
	}
	metadata := manifest{
		Version:                        formatVersion,
		SnapshotVersion:                snapshot.Version,
		TransferID:                     transferID,
		Digest:                         digest,
		Components:                     hashes,
		CallbackOperationStateIncluded: true,
		CallbackSigningSecretsIncluded: false,
	}
	encoded, err := marshalCanonical(metadata)
	if err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	if err := writeSynced(filepath.Join(temporary, "manifest.json"), encoded); err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	if err := syncDirectory(temporary); err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	if err := syncDirectory(filepath.Join(store.root, "versions")); err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	_, reread, err := store.readVersion(digest)
	if err != nil {
		return coordinatortransfer.SnapshotReceipt{}, fmt.Errorf("reread staged coordinator bundle: %w", err)
	}
	if reread != receipt {
		return coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
	}
	return receipt, nil
}

func (store *Store) Apply(ctx context.Context, transferID, digest string) error {
	if store == nil || ctx == nil || !safeID(transferID) || !validDigest(digest) {
		return ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireNoRecovery(); err != nil {
		return err
	}
	_, receipt, err := store.readVersion(digest)
	if err != nil || receipt.TransferID != transferID {
		return ErrInvalidState
	}
	var previous *pointer
	if current, err := store.readPointer(); err == nil {
		if current.TransferID == transferID && current.Digest == digest {
			active, activeReceipt, readErr := store.readActive()
			activeDigest, digestErr := active.Digest()
			if readErr == nil && digestErr == nil && activeReceipt == receipt && activeDigest == digest {
				return nil
			}
			return ErrInvalidState
		}
		previous = &current
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next := pointer{Version: formatVersion, TransferID: transferID, Digest: digest}
	if previous != nil {
		next.PreviousTransferID, next.PreviousDigest = previous.TransferID, previous.Digest
	}
	if err := writeAtomicJSON(store.markerPath(), activationMarker{Version: formatVersion, Previous: previous, Next: next}); err != nil {
		return err
	}
	if err := writeAtomicJSON(store.pointerPath(), next); err != nil {
		return err
	}
	snapshot, activeReceipt, err := store.readActive()
	activeDigest, digestErr := snapshot.Digest()
	if err != nil || digestErr != nil || activeReceipt != receipt || activeDigest != digest {
		return ErrRecoveryRequired
	}
	if err := os.Remove(store.markerPath()); err != nil {
		return ErrRecoveryRequired
	}
	return syncDirectory(store.root)
}

func (store *Store) Read(ctx context.Context) (coordinatorbundle.Bundle, coordinatortransfer.SnapshotReceipt, error) {
	if store == nil || ctx == nil {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireNoRecovery(); err != nil {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, err
	}
	return store.readActive()
}

func (store *Store) Rollback(ctx context.Context, transferID string) error {
	if store == nil || ctx == nil {
		return ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireNoRecovery(); err != nil {
		return err
	}
	current, err := store.readPointer()
	if err != nil || current.TransferID != transferID || current.PreviousDigest == "" {
		return ErrInvalidState
	}
	previous := pointer{Version: formatVersion, TransferID: current.PreviousTransferID, Digest: current.PreviousDigest}
	if _, receipt, err := store.readVersion(previous.Digest); err != nil || receipt.TransferID != previous.TransferID {
		return ErrInvalidState
	}
	if err := writeAtomicJSON(store.markerPath(), activationMarker{Version: formatVersion, Previous: &current, Next: previous}); err != nil {
		return err
	}
	if err := writeAtomicJSON(store.pointerPath(), previous); err != nil {
		return err
	}
	_, receipt, err := store.readActive()
	if err != nil || receipt.TransferID != previous.TransferID || receipt.Digest != previous.Digest {
		return ErrRecoveryRequired
	}
	if err := os.Remove(store.markerPath()); err != nil {
		return ErrRecoveryRequired
	}
	return syncDirectory(store.root)
}

func RecoverInterruptedRollback(root string) error {
	if strings.TrimSpace(root) == "" {
		return ErrInvalidState
	}
	absolute, err := filepath.Abs(root)
	if err != nil || filepath.Dir(absolute) == absolute {
		return ErrInvalidState
	}
	if err := requirePrivateDirectory(absolute); err != nil {
		return err
	}
	if err := requirePrivateDirectory(filepath.Join(absolute, "versions")); err != nil {
		return err
	}
	var marker activationMarker
	if err := readStrictJSON(filepath.Join(absolute, "activation.json"), maxMetadataSize, &marker); err != nil {
		return err
	}
	if marker.Version != formatVersion || marker.Next.Version != formatVersion || !safeID(marker.Next.TransferID) || !validDigest(marker.Next.Digest) {
		return ErrInvalidState
	}
	store := &Store{root: absolute}
	if marker.Previous == nil {
		if err := os.Remove(filepath.Join(absolute, "active.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		if marker.Previous.Version != formatVersion || !safeID(marker.Previous.TransferID) || !validDigest(marker.Previous.Digest) {
			return ErrInvalidState
		}
		if _, receipt, readErr := store.readVersion(marker.Previous.Digest); readErr != nil || receipt.TransferID != marker.Previous.TransferID {
			return ErrInvalidState
		}
		if err := writeAtomicJSON(filepath.Join(absolute, "active.json"), *marker.Previous); err != nil {
			return err
		}
		if _, receipt, readErr := store.readActive(); readErr != nil || receipt.TransferID != marker.Previous.TransferID || receipt.Digest != marker.Previous.Digest {
			return ErrInvalidState
		}
	}
	if err := os.Remove(filepath.Join(absolute, "activation.json")); err != nil {
		return err
	}
	return syncDirectory(absolute)
}

func (store *Store) readActive() (coordinatorbundle.Bundle, coordinatortransfer.SnapshotReceipt, error) {
	activePointer, err := store.readPointer()
	if err != nil {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, err
	}
	snapshot, receipt, err := store.readVersion(activePointer.Digest)
	if err != nil || receipt.TransferID != activePointer.TransferID || receipt.Digest != activePointer.Digest {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
	}
	return snapshot, receipt, nil
}

func (store *Store) readPointer() (pointer, error) {
	var current pointer
	if err := readStrictJSON(store.pointerPath(), maxMetadataSize, &current); err != nil {
		return pointer{}, err
	}
	if current.Version != formatVersion || !safeID(current.TransferID) || !validDigest(current.Digest) {
		return pointer{}, ErrInvalidState
	}
	return current, nil
}

func (store *Store) readVersion(digest string) (coordinatorbundle.Bundle, coordinatortransfer.SnapshotReceipt, error) {
	if !validDigest(digest) {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
	}
	dir := store.versionPath(digest)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
	}
	var metadata manifest
	if err := readStrictJSON(filepath.Join(dir, "manifest.json"), maxMetadataSize, &metadata); err != nil {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, err
	}
	if metadata.Version != formatVersion || metadata.SnapshotVersion != coordinatorbundle.Version || metadata.Digest != digest || !safeID(metadata.TransferID) || !metadata.CallbackOperationStateIncluded || metadata.CallbackSigningSecretsIncluded || len(metadata.Components) != len(componentNames) {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
	}
	var snapshot coordinatorbundle.Bundle
	targets := map[string]any{
		"catalog.json": &snapshot.Catalog, "routes.json": &snapshot.Routes,
		"settings.json": &snapshot.Settings, "sessions.json": &snapshot.Sessions, "telegram-scope.json": &snapshot.TelegramScope,
		"telegram-ui.json": &snapshot.TelegramUI, "journals.json": &snapshot.Journals,
		"inputs.json": &snapshot.Inputs, "outputs.json": &snapshot.Outputs,
		"checkpoint.json": &snapshot.Checkpoint, "callback-key-id.json": &snapshot.CallbackVerificationKeyID,
		"callback-registry.json":   &snapshot.CallbackRegistry,
		"callback-operations.json": &snapshot.CallbackOperations,
	}
	for _, name := range componentNames {
		data, err := readBoundedRegular(filepath.Join(dir, name), maxComponentSize)
		if err != nil || hashBytes(data) != metadata.Components[name] {
			return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
		}
		if err := decodeStrict(data, targets[name]); err != nil {
			return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, err
		}
	}
	snapshot.Version = metadata.SnapshotVersion
	if err := validateSnapshot(snapshot); err != nil {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, err
	}
	gotDigest, _ := snapshot.Digest()
	if gotDigest != digest {
		return coordinatorbundle.Bundle{}, coordinatortransfer.SnapshotReceipt{}, ErrInvalidState
	}
	return snapshot, coordinatortransfer.SnapshotReceipt{TransferID: metadata.TransferID, Digest: digest}, nil
}

func validateSnapshot(snapshot coordinatorbundle.Bundle) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	checkpoint := snapshot.Checkpoint
	if checkpoint.Revision == 0 {
		return ErrInvalidState
	}
	if checkpoint.Checkpoint.Blocked != nil {
		blocked := checkpoint.Checkpoint.Blocked
		if blocked.UpdateID <= 0 || blocked.UpdateID < checkpoint.Checkpoint.NextUpdateID || strings.TrimSpace(blocked.Reason) == "" {
			return ErrInvalidState
		}
	}
	if outbound := checkpoint.Checkpoint.Outbound; outbound != nil {
		if strings.TrimSpace(outbound.OperationID) == "" || outbound.UpdateID <= 0 || outbound.Status.ConversationID <= 0 || strings.TrimSpace(outbound.Status.Text) == "" {
			return ErrInvalidState
		}
		switch string(outbound.Phase) {
		case "prepared", "unknown":
			if outbound.Receipt != nil || outbound.UpdateID < checkpoint.Checkpoint.NextUpdateID {
				return ErrInvalidState
			}
		case "confirmed":
			if outbound.Receipt == nil || outbound.Receipt.MessageID <= 0 || outbound.UpdateID == math.MaxInt64 || checkpoint.Checkpoint.NextUpdateID <= outbound.UpdateID {
				return ErrInvalidState
			}
		default:
			return ErrInvalidState
		}
	}
	for _, input := range snapshot.Inputs {
		if sensitive(input.Payload) {
			return ErrSensitiveState
		}
	}
	for _, output := range snapshot.Outputs {
		if sensitive(output.Payload) || sensitive([]byte(output.Receipt)) {
			return ErrSensitiveState
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return ErrInvalidState
	}
	if sensitive(encoded) {
		return ErrSensitiveState
	}
	return nil
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN(?: [A-Z0-9]+)? PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\bauthorization\s*:\s*(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`[0-9]{8,12}:[A-Za-z0-9_-]{30,}`),
	regexp.MustCompile(`(?i)(?:[A-Z0-9_]*(?:API_KEY|TOKEN|PASSWORD|PASSPHRASE|SECRET|OAUTH|AUTH)[A-Z0-9_]*)\s*[=:]\s*["']?[A-Za-z0-9._~+/@:-]+`),
	regexp.MustCompile(`(?i)"(?:api_key|access_token|refresh_token|client_secret|password|passphrase|oauth_token|bot_token|telegram_token|callback_secret|credential_ref|secret_ref|secret_file|env_var)"\s*:\s*"[^"\r\n]+"`),
	regexp.MustCompile(`(?i)(?:secret|keychain|credential)://[^\s"']+`),
}

func sensitive(data []byte) bool {
	for _, pattern := range secretPatterns {
		if pattern.Match(data) {
			return true
		}
	}
	lower := bytes.ToLower(data)
	for _, marker := range [][]byte{[]byte("credentials/"), []byte(".credentials"), []byte("/run/secrets/")} {
		if bytes.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (store *Store) pointerPath() string { return filepath.Join(store.root, "active.json") }
func (store *Store) markerPath() string  { return filepath.Join(store.root, "activation.json") }
func (store *Store) versionPath(digest string) string {
	return filepath.Join(store.root, "versions", digest)
}

func (store *Store) requireNoRecovery() error {
	if _, err := os.Lstat(store.markerPath()); err == nil {
		return ErrRecoveryRequired
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func safeID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}
func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
func hashBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func marshalCanonical(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrInvalidState
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidState
	}
	return nil
}
func readStrictJSON(path string, limit int64, value any) error {
	data, err := readBoundedRegular(path, limit)
	if err != nil {
		return err
	}
	return decodeStrict(data, value)
}

func readBoundedRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, ErrInvalidState
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != info.Size() || opened.Size() > limit {
		return nil, ErrInvalidState
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrInvalidState
	}
	return data, nil
}

func requirePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
		return ErrInvalidState
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
		verified, err := os.Lstat(path)
		if err != nil || !verified.IsDir() || verified.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(verified) || verified.Mode().Perm() != 0o700 {
			return ErrInvalidState
		}
	}
	return nil
}

func preparePrivateRoot(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(root, 0o700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
		return ErrInvalidState
	}
	if err := requirePrivateDirectory(root); err != nil {
		return err
	}
	versions := filepath.Join(root, "versions")
	info, err = os.Lstat(versions)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(versions, 0o700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
		return ErrInvalidState
	}
	return requirePrivateDirectory(versions)
}

func writeSynced(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
func writeAtomicJSON(path string, value any) error {
	data, err := marshalCanonical(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".pointer-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
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
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	verified, err := readBoundedRegular(path, maxMetadataSize)
	if err != nil || !bytes.Equal(data, verified) {
		return ErrInvalidState
	}
	return nil
}
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	return errors.Join(err, closeErr)
}
