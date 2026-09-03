package messagejournal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
)

const formatVersion = 2

const (
	hardMaxSessions          = 1024
	hardMaxRecordsPerSession = 65536
	hardMaxPayloadBytes      = 4 << 20
	hardMaxIDBytes           = 512
	hardMaxKindBytes         = 128
	hardMaxReceiptBytes      = 16 << 10
	hardMaxFileBytes         = 256 << 20
)

type Journal struct {
	path     string
	lockPath string
	limits   Limits
	mu       *sync.Mutex
}

type document struct {
	Version  int             `json:"version"`
	Sessions []sessionRecord `json:"sessions"`
}

type sessionRecord struct {
	SessionID    string         `json:"session_id"`
	NextSequence uint64         `json:"next_sequence"`
	Inputs       []inputRecord  `json:"inputs"`
	Outputs      []outputRecord `json:"outputs"`
}

type inputRecord struct {
	MessageID   string             `json:"message_id"`
	Sequence    uint64             `json:"sequence"`
	Payload     []byte             `json:"payload"`
	Attachments []attachmentRecord `json:"attachments,omitempty"`
	Phase       InputPhase         `json:"phase"`
	Lease       leaseRecord        `json:"lease"`
}

type attachmentRecord struct {
	Reference string `json:"reference"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

type outputRecord struct {
	OperationID string      `json:"operation_id"`
	Sequence    uint64      `json:"sequence"`
	Kind        string      `json:"kind"`
	Payload     []byte      `json:"payload"`
	Phase       OutputPhase `json:"phase"`
	Receipt     string      `json:"receipt,omitempty"`
	Lease       leaseRecord `json:"lease"`
}

type leaseRecord struct {
	Owner     string `json:"owner,omitempty"`
	UntilUnix int64  `json:"until_unix_nano,omitempty"`
}

var pathLocks = struct {
	sync.Mutex
	byPath map[string]*sync.Mutex
}{byPath: make(map[string]*sync.Mutex)}

// Open validates the complete existing journal without creating it. Stores
// opened for the same path share process-local serialization and reread the
// file before every operation, preventing lost updates between instances.
func Open(path string, limits Limits) (*Journal, error) {
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("message journal path is required")
	}
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("resolve message journal path: %w", err)
	}
	parent, err := os.Stat(filepath.Dir(absPath))
	if err != nil {
		return nil, fmt.Errorf("stat message journal directory: %w", err)
	}
	if !parent.IsDir() {
		return nil, errors.New("message journal parent is not a directory")
	}
	pathLocks.Lock()
	mu := pathLocks.byPath[absPath]
	if mu == nil {
		mu = &sync.Mutex{}
		pathLocks.byPath[absPath] = mu
	}
	pathLocks.Unlock()

	journal := &Journal{path: absPath, lockPath: absPath + ".lock", limits: limits, mu: mu}
	mu.Lock()
	defer mu.Unlock()
	if _, err := journal.read(); err != nil {
		return nil, err
	}
	return journal, nil
}

func validateLimits(limits Limits) error {
	if limits.MaxSessions < 1 || limits.MaxSessions > hardMaxSessions {
		return errors.New("max sessions is outside the supported range")
	}
	if limits.MaxInputsPerSession < 1 || limits.MaxInputsPerSession > hardMaxRecordsPerSession {
		return errors.New("max inputs per session is outside the supported range")
	}
	if limits.MaxPendingInputsPerSession < 1 || limits.MaxPendingInputsPerSession > limits.MaxInputsPerSession {
		return errors.New("max pending inputs is outside the supported range")
	}
	if limits.MaxOutputsPerSession < 1 || limits.MaxOutputsPerSession > hardMaxRecordsPerSession {
		return errors.New("max outputs per session is outside the supported range")
	}
	if limits.MaxPayloadBytes < 1 || limits.MaxPayloadBytes > hardMaxPayloadBytes ||
		limits.MaxIDBytes < 1 || limits.MaxIDBytes > hardMaxIDBytes ||
		limits.MaxKindBytes < 1 || limits.MaxKindBytes > hardMaxKindBytes ||
		limits.MaxReceiptBytes < 1 || limits.MaxReceiptBytes > hardMaxReceiptBytes {
		return errors.New("message journal text limit is outside the supported range")
	}
	if limits.MaxFileBytes < 1024 || limits.MaxFileBytes > hardMaxFileBytes {
		return errors.New("max journal file bytes is outside the supported range")
	}
	if limits.MaxLeaseDuration <= 0 || limits.MaxLeaseDuration > DefaultLimits().MaxLeaseDuration {
		return errors.New("max lease duration is outside the supported range")
	}
	return nil
}

func (journal *Journal) read() (document, error) {
	file, err := os.Open(journal.path)
	if errors.Is(err, os.ErrNotExist) {
		return document{Version: formatVersion, Sessions: []sessionRecord{}}, nil
	}
	if err != nil {
		return document{}, fmt.Errorf("open message journal: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return document{}, fmt.Errorf("stat message journal: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > journal.limits.MaxFileBytes {
		return document{}, fmt.Errorf("%w: journal is not a bounded regular file", ErrInvalidFormat)
	}
	data, err := io.ReadAll(io.LimitReader(file, journal.limits.MaxFileBytes+1))
	if err != nil {
		return document{}, fmt.Errorf("read message journal: %w", err)
	}
	if int64(len(data)) > journal.limits.MaxFileBytes {
		return document{}, fmt.Errorf("%w: journal exceeds byte limit", ErrInvalidFormat)
	}
	var loaded document
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&loaded); err != nil {
		return document{}, fmt.Errorf("%w: decode: %v", ErrInvalidFormat, err)
	}
	if loaded.Version == 1 {
		loaded.Version = formatVersion
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return document{}, fmt.Errorf("%w: trailing JSON value", ErrInvalidFormat)
	}
	if err := validateDocument(loaded, journal.limits); err != nil {
		return document{}, fmt.Errorf("%w: %v", ErrInvalidFormat, err)
	}
	return loaded, nil
}

func (journal *Journal) mutate(change func(*document) error) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return withExclusiveFileLock(journal.lockPath, func() error {
		loaded, err := journal.read()
		if err != nil {
			return err
		}
		if err := change(&loaded); err != nil {
			return err
		}
		if err := validateDocument(loaded, journal.limits); err != nil {
			return fmt.Errorf("validate message journal mutation: %w", err)
		}
		data, err := json.Marshal(loaded)
		if err != nil {
			return fmt.Errorf("encode message journal: %w", err)
		}
		if int64(len(data)) > journal.limits.MaxFileBytes {
			return ErrJournalFull
		}
		if err := atomicWrite(journal.path, data); err != nil {
			return fmt.Errorf("persist message journal: %w", err)
		}
		persisted, err := journal.read()
		if err != nil {
			return fmt.Errorf("reread message journal: %w", err)
		}
		if !reflect.DeepEqual(persisted, loaded) {
			return errors.New("reread message journal: persisted value mismatch")
		}
		return nil
	})
}

func (journal *Journal) inspect(read func(document) error) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	loaded, err := journal.read()
	if err != nil {
		return err
	}
	return read(loaded)
}

func atomicWrite(path string, data []byte) (returnErr error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := temporary.Close(); returnErr == nil && closeErr != nil {
				returnErr = closeErr
			}
		}
		if removeErr := os.Remove(temporaryPath); returnErr == nil && removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			returnErr = removeErr
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return directoryFile.Sync()
}

func validateDocument(loaded document, limits Limits) error {
	if loaded.Version != formatVersion {
		return fmt.Errorf("unsupported version %d", loaded.Version)
	}
	if loaded.Sessions == nil || len(loaded.Sessions) > limits.MaxSessions {
		return errors.New("session collection is missing or exceeds its limit")
	}
	seenSessions := make(map[string]struct{}, len(loaded.Sessions))
	for _, session := range loaded.Sessions {
		if err := validateOpaqueID(session.SessionID, limits.MaxIDBytes, "session id"); err != nil {
			return err
		}
		if _, duplicate := seenSessions[session.SessionID]; duplicate {
			return errors.New("duplicate session id")
		}
		seenSessions[session.SessionID] = struct{}{}
		if session.Inputs == nil || len(session.Inputs) > limits.MaxInputsPerSession ||
			session.Outputs == nil || len(session.Outputs) > limits.MaxOutputsPerSession {
			return errors.New("session record collection is missing or exceeds its limit")
		}
		if err := validateSession(session, limits); err != nil {
			return fmt.Errorf("session %q: %w", session.SessionID, err)
		}
	}
	return nil
}

func validateSession(session sessionRecord, limits Limits) error {
	sequences := make([]uint64, 0, len(session.Inputs)+len(session.Outputs))
	messageIDs := make(map[string]struct{}, len(session.Inputs))
	pendingInputs := 0
	var previousInputSequence uint64
	for _, input := range session.Inputs {
		if err := validateOpaqueID(input.MessageID, limits.MaxIDBytes, "message id"); err != nil {
			return err
		}
		if _, duplicate := messageIDs[input.MessageID]; duplicate {
			return errors.New("duplicate message id")
		}
		messageIDs[input.MessageID] = struct{}{}
		if input.Sequence == 0 || input.Sequence <= previousInputSequence || len(input.Payload) > limits.MaxPayloadBytes {
			return errors.New("invalid input sequence or payload")
		}
		for _, attachment := range input.Attachments {
			if err := validateAttachmentRecord(attachment, limits); err != nil {
				return err
			}
		}
		previousInputSequence = input.Sequence
		if input.Phase == InputPending {
			pendingInputs++
		}
		switch input.Phase {
		case InputPending:
			if err := validateLease(input.Lease, limits); err != nil {
				return err
			}
		case InputAccepted, InputCompleted, InputFailed, InputUnknown:
			if input.Lease != (leaseRecord{}) {
				return errors.New("terminal or accepted input retains a lease")
			}
		default:
			return errors.New("unsupported input phase")
		}
		sequences = append(sequences, input.Sequence)
	}
	if pendingInputs > limits.MaxPendingInputsPerSession {
		return errors.New("pending input count exceeds queue limit")
	}
	operationIDs := make(map[string]struct{}, len(session.Outputs))
	var previousOutputSequence uint64
	for _, output := range session.Outputs {
		if err := validateOpaqueID(output.OperationID, limits.MaxIDBytes, "operation id"); err != nil {
			return err
		}
		if _, duplicate := operationIDs[output.OperationID]; duplicate {
			return errors.New("duplicate operation id")
		}
		operationIDs[output.OperationID] = struct{}{}
		if output.Sequence == 0 || output.Sequence <= previousOutputSequence || len(output.Payload) > limits.MaxPayloadBytes ||
			strings.TrimSpace(output.Kind) == "" || len(output.Kind) > limits.MaxKindBytes {
			return errors.New("invalid output sequence, kind, or payload")
		}
		previousOutputSequence = output.Sequence
		switch output.Phase {
		case OutputPending:
			if output.Receipt != "" {
				return errors.New("pending output has a receipt")
			}
			if err := validateLease(output.Lease, limits); err != nil {
				return err
			}
		case OutputConfirmed:
			if output.Lease != (leaseRecord{}) || strings.TrimSpace(output.Receipt) == "" || len(output.Receipt) > limits.MaxReceiptBytes {
				return errors.New("confirmed output has invalid receipt or lease")
			}
		case OutputFailed, OutputUnknown:
			if output.Lease != (leaseRecord{}) || output.Receipt != "" {
				return errors.New("failed or unknown output has a receipt or lease")
			}
		default:
			return errors.New("unsupported output phase")
		}
		sequences = append(sequences, output.Sequence)
	}
	sort.Slice(sequences, func(left, right int) bool { return sequences[left] < sequences[right] })
	if uint64(len(sequences)) != session.NextSequence {
		return errors.New("next sequence does not match record count")
	}
	for index, sequence := range sequences {
		if sequence != uint64(index+1) {
			return errors.New("session sequence is not contiguous and monotonic")
		}
	}
	return nil
}

func validateAttachmentRecord(attachment attachmentRecord, limits Limits) error {
	if strings.TrimSpace(attachment.Reference) == "" || attachment.Reference != strings.TrimSpace(attachment.Reference) ||
		len(attachment.Reference) > limits.MaxIDBytes || filepath.IsAbs(attachment.Reference) ||
		strings.ContainsAny(attachment.Reference, `/\\`) || attachment.Size <= 0 || len(attachment.SHA256) != 64 {
		return errors.New("invalid input attachment reference")
	}
	for _, character := range attachment.SHA256 {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return errors.New("invalid input attachment digest")
		}
	}
	return nil
}

func validateLease(lease leaseRecord, limits Limits) error {
	if lease == (leaseRecord{}) {
		return nil
	}
	if err := validateOpaqueID(lease.Owner, limits.MaxIDBytes, "lease owner"); err != nil {
		return err
	}
	if lease.UntilUnix <= 0 {
		return errors.New("lease expiry must be positive")
	}
	return nil
}

func validateOpaqueID(value string, maxBytes int, name string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maxBytes {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func sessionAt(loaded *document, sessionID string, create bool, limits Limits) (*sessionRecord, error) {
	for index := range loaded.Sessions {
		if loaded.Sessions[index].SessionID == sessionID {
			return &loaded.Sessions[index], nil
		}
	}
	if !create {
		return nil, ErrNotFound
	}
	if len(loaded.Sessions) == limits.MaxSessions {
		return nil, ErrJournalFull
	}
	loaded.Sessions = append(loaded.Sessions, sessionRecord{
		SessionID: sessionID,
		Inputs:    []inputRecord{},
		Outputs:   []outputRecord{},
	})
	return &loaded.Sessions[len(loaded.Sessions)-1], nil
}
