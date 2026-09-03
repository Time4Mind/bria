package interactionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/interactionsourcestore"
	"bria/internal/runtimeprotocol"
)

const fileStoreVersion = 1
const maxStoreBytes = 16 << 20
const maxOperations = 10_000

var ErrOperationExists = errors.New("provider interaction operation already exists")
var ErrImmutableIdentity = errors.New("provider interaction immutable identity changed")
var ErrInvalidOperation = errors.New("invalid provider interaction operation")
var ErrInvalidTransition = errors.New("invalid provider interaction phase transition")
var ErrStoreExhausted = errors.New("provider interaction store capacity exhausted")

type Phase string

const (
	PhasePrepared                  Phase = "prepared"
	PhaseSendUnknown               Phase = "send_unknown"
	PhaseWaiting                   Phase = "waiting"
	PhaseWaitingText               Phase = "waiting_text"
	PhaseSecretDeletionUnknown     Phase = "secret_deletion_unknown"
	PhaseResponseReady             Phase = "response_ready"
	PhaseProviderResponseUnknown   Phase = "provider_response_unknown"
	PhaseProviderResponseConfirmed Phase = "provider_response_confirmed"
)

type Operation struct {
	ID                    string                               `json:"id"`
	SessionID             domain.SessionID                     `json:"session_id"`
	MessageID             string                               `json:"message_id"`
	ProviderRequestID     string                               `json:"provider_request_id"`
	ConversationID        int64                                `json:"conversation_id"`
	CarrierMessageID      int64                                `json:"carrier_message_id,omitempty"`
	Request               runtimeprotocol.InteractionRequest   `json:"request"`
	Phase                 Phase                                `json:"phase"`
	QuestionIndex         int                                  `json:"question_index,omitempty"`
	Answers               map[string][]string                  `json:"answers"`
	Response              *runtimeprotocol.InteractionResponse `json:"response,omitempty"`
	Resolution            string                               `json:"resolution,omitempty"`
	LastCallbackID        string                               `json:"last_callback_id,omitempty"`
	SecretSourceMessageID int64                                `json:"secret_source_message_id,omitempty"`
	SecretResponse        bool                                 `json:"secret_response,omitempty"`
	Revision              uint64                               `json:"revision"`
	CreatedAt             time.Time                            `json:"created_at"`
	UpdatedAt             time.Time                            `json:"updated_at"`
}

type ConsumedSource = interactionsourcestore.Source
type Store interface {
	Load(context.Context, string) (Operation, bool, error)
	PendingText(context.Context, int64) (Operation, bool, error)
	LoadConsumedSource(context.Context, int64, int64, int64) (ConsumedSource, bool, error)
	RecordConsumedSource(context.Context, ConsumedSource) (ConsumedSource, error)
	ConfirmConsumedSourceDeletion(context.Context, ConsumedSource, uint64) (ConsumedSource, bool, error)
	Create(context.Context, Operation) (Operation, error)
	CompareAndSwap(context.Context, string, uint64, Operation) (Operation, bool, error)
	DeleteConfirmed(context.Context, string, uint64) (bool, error)
}

type fileState struct {
	Version    int                  `json:"version"`
	Operations map[string]Operation `json:"operations"`
}

type FileStore struct {
	mu            sync.Mutex
	path          string
	syncDirectory func(string) error
	state         fileState
	sources       interactionsourcestore.Store
}

func OpenFileStore(path string) (*FileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("provider interaction store path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve provider interaction store path: %w", err)
	}
	sources, err := interactionsourcestore.OpenFileStore(absolute + ".sources")
	if err != nil {
		return nil, fmt.Errorf("open consumed interaction sources: %w", err)
	}
	store := &FileStore{
		path: absolute, syncDirectory: syncStoreDirectory,
		state: fileState{Version: fileStoreVersion, Operations: make(map[string]Operation)}, sources: sources,
	}
	info, err := os.Stat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat provider interaction store: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxStoreBytes {
		return nil, errors.New("provider interaction store must be a bounded regular file")
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, fmt.Errorf("read provider interaction store: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store.state); err != nil {
		return nil, fmt.Errorf("decode provider interaction store: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return nil, err
	}
	if err := validateState(store.state); err != nil {
		return nil, fmt.Errorf("validate provider interaction store: %w", err)
	}
	if pruneConfirmed(store.state.Operations) {
		if err := writeState(store.path, store.state, store.syncDirectory); err != nil {
			return nil, fmt.Errorf("prune confirmed provider interactions: %w", err)
		}
	}
	return store, nil
}

func (store *FileStore) Load(ctx context.Context, id string) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	if store == nil || !saneText(id, 256) {
		return Operation{}, false, ErrInvalidOperation
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, found := store.state.Operations[id]
	return cloneOperation(operation), found, nil
}

func (store *FileStore) PendingText(ctx context.Context, conversationID int64) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	if store == nil || conversationID <= 0 {
		return Operation{}, false, ErrInvalidOperation
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return pendingTextOperation(store.state.Operations, conversationID)
}

func (store *FileStore) LoadConsumedSource(ctx context.Context, actorID, conversationID, messageID int64) (ConsumedSource, bool, error) {
	if store == nil || store.sources == nil {
		return ConsumedSource{}, false, ErrInvalidOperation
	}
	return store.sources.Load(ctx, actorID, conversationID, messageID)
}

func (store *FileStore) RecordConsumedSource(ctx context.Context, source ConsumedSource) (ConsumedSource, error) {
	if store == nil || store.sources == nil {
		return ConsumedSource{}, ErrInvalidOperation
	}
	return store.sources.Record(ctx, source)
}

func (store *FileStore) ConfirmConsumedSourceDeletion(ctx context.Context, source ConsumedSource, revision uint64) (ConsumedSource, bool, error) {
	if store == nil || store.sources == nil {
		return ConsumedSource{}, false, ErrInvalidOperation
	}
	return store.sources.ConfirmDeletion(ctx, source, revision)
}

func (store *FileStore) Create(ctx context.Context, operation Operation) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, err
	}
	if store == nil {
		return Operation{}, ErrInvalidOperation
	}
	operation = cloneOperation(operation)
	operation.Revision = 1
	if err := validateOperation(operation); err != nil {
		return Operation{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.state.Operations[operation.ID]; exists {
		return Operation{}, ErrOperationExists
	}
	if len(store.state.Operations) >= maxOperations {
		return Operation{}, ErrStoreExhausted
	}
	next := cloneState(store.state)
	next.Operations[operation.ID] = cloneOperation(operation)
	if err := writeState(store.path, next, store.syncDirectory); err != nil {
		return Operation{}, fmt.Errorf("persist provider interaction: %w", err)
	}
	store.state = next
	return cloneOperation(operation), nil
}

func (store *FileStore) CompareAndSwap(ctx context.Context, id string, revision uint64, next Operation) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	if store == nil || !saneText(id, 256) || revision == 0 || next.ID != id {
		return Operation{}, false, ErrInvalidOperation
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, found := store.state.Operations[id]
	if !found || current.Revision != revision {
		return cloneOperation(current), false, nil
	}
	if !sameIdentity(current, next) {
		return Operation{}, false, ErrImmutableIdentity
	}
	if !validTransition(current.Phase, next.Phase) {
		return Operation{}, false, ErrInvalidTransition
	}
	if next.Phase == PhaseWaitingText && current.Phase != PhaseWaitingText && hasOtherPendingText(store.state.Operations, id) {
		return Operation{}, false, ErrInvalidTransition
	}
	next = cloneOperation(next)
	next.Revision = current.Revision + 1
	if next.UpdatedAt.Before(current.UpdatedAt) {
		return Operation{}, false, ErrInvalidOperation
	}
	if err := validateOperation(next); err != nil {
		return Operation{}, false, err
	}
	state := cloneState(store.state)
	state.Operations[id] = cloneOperation(next)
	if err := writeState(store.path, state, store.syncDirectory); err != nil {
		return Operation{}, false, fmt.Errorf("persist provider interaction transition: %w", err)
	}
	store.state = state
	return cloneOperation(next), true, nil
}

func (store *FileStore) DeleteConfirmed(ctx context.Context, id string, revision uint64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil || !saneText(id, 256) || revision == 0 {
		return false, ErrInvalidOperation
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, found := store.state.Operations[id]
	if !found || current.Revision != revision {
		return false, nil
	}
	if current.Phase != PhaseProviderResponseConfirmed {
		return false, ErrInvalidTransition
	}
	next := cloneState(store.state)
	delete(next.Operations, id)
	if err := writeState(store.path, next, store.syncDirectory); err != nil {
		return false, fmt.Errorf("prune confirmed provider interaction: %w", err)
	}
	store.state = next
	return true, nil
}

type MemoryStore struct {
	mu         sync.Mutex
	operations map[string]Operation
	sources    interactionsourcestore.Store
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{operations: make(map[string]Operation), sources: interactionsourcestore.NewMemoryStore()}
}

func (store *MemoryStore) LoadConsumedSource(ctx context.Context, actorID, conversationID, messageID int64) (ConsumedSource, bool, error) {
	if store == nil || store.sources == nil {
		return ConsumedSource{}, false, ErrInvalidOperation
	}
	return store.sources.Load(ctx, actorID, conversationID, messageID)
}

func (store *MemoryStore) RecordConsumedSource(ctx context.Context, source ConsumedSource) (ConsumedSource, error) {
	if store == nil || store.sources == nil {
		return ConsumedSource{}, ErrInvalidOperation
	}
	return store.sources.Record(ctx, source)
}

func (store *MemoryStore) ConfirmConsumedSourceDeletion(ctx context.Context, source ConsumedSource, revision uint64) (ConsumedSource, bool, error) {
	if store == nil || store.sources == nil {
		return ConsumedSource{}, false, ErrInvalidOperation
	}
	return store.sources.ConfirmDeletion(ctx, source, revision)
}

func (store *MemoryStore) Load(ctx context.Context, id string) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, found := store.operations[id]
	return cloneOperation(operation), found, nil
}

func (store *MemoryStore) PendingText(ctx context.Context, conversationID int64) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	if store == nil || conversationID <= 0 {
		return Operation{}, false, ErrInvalidOperation
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return pendingTextOperation(store.operations, conversationID)
}

func (store *MemoryStore) Create(ctx context.Context, operation Operation) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, err
	}
	operation = cloneOperation(operation)
	operation.Revision = 1
	if err := validateOperation(operation); err != nil {
		return Operation{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.operations[operation.ID]; exists {
		return Operation{}, ErrOperationExists
	}
	if len(store.operations) >= maxOperations {
		return Operation{}, ErrStoreExhausted
	}
	store.operations[operation.ID] = cloneOperation(operation)
	return cloneOperation(operation), nil
}

func (store *MemoryStore) CompareAndSwap(ctx context.Context, id string, revision uint64, next Operation) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, found := store.operations[id]
	if !found || current.Revision != revision {
		return cloneOperation(current), false, nil
	}
	if !sameIdentity(current, next) {
		return Operation{}, false, ErrImmutableIdentity
	}
	if !validTransition(current.Phase, next.Phase) {
		return Operation{}, false, ErrInvalidTransition
	}
	if next.Phase == PhaseWaitingText && current.Phase != PhaseWaitingText && hasOtherPendingText(store.operations, id) {
		return Operation{}, false, ErrInvalidTransition
	}
	next = cloneOperation(next)
	next.Revision = current.Revision + 1
	if next.UpdatedAt.Before(current.UpdatedAt) {
		return Operation{}, false, ErrInvalidOperation
	}
	if err := validateOperation(next); err != nil {
		return Operation{}, false, err
	}
	store.operations[id] = cloneOperation(next)
	return cloneOperation(next), true, nil
}

func (store *MemoryStore) DeleteConfirmed(ctx context.Context, id string, revision uint64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, found := store.operations[id]
	if !found || current.Revision != revision {
		return false, nil
	}
	if current.Phase != PhaseProviderResponseConfirmed {
		return false, ErrInvalidTransition
	}
	delete(store.operations, id)
	return true, nil
}

func validateState(state fileState) error {
	if state.Version != fileStoreVersion || state.Operations == nil || len(state.Operations) > maxOperations {
		return ErrInvalidOperation
	}
	pendingText := 0
	for id, operation := range state.Operations {
		if id != operation.ID {
			return ErrInvalidOperation
		}
		if err := validateOperation(operation); err != nil {
			return err
		}
		if pendingTextPhase(operation.Phase) {
			pendingText++
		}
	}
	if pendingText > 1 {
		return ErrInvalidOperation
	}
	return nil
}

func validateOperation(operation Operation) error {
	if !saneText(operation.ID, 256) || !saneText(string(operation.SessionID), 256) ||
		!saneText(operation.MessageID, 1024) || !saneText(operation.ProviderRequestID, 1024) ||
		operation.Request.ID != operation.ProviderRequestID || operation.ConversationID <= 0 || operation.Revision == 0 ||
		operation.CreatedAt.IsZero() || operation.UpdatedAt.Before(operation.CreatedAt) {
		return ErrInvalidOperation
	}
	if _, err := runtimeprotocol.EncodeAdapterLine(runtimeprotocol.AdapterMessage{
		Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeInteractionRequest,
		RequestID: "validation", InteractionRequest: &operation.Request,
	}, runtimeprotocol.Limits{}); err != nil {
		return ErrInvalidOperation
	}
	if operation.QuestionIndex < 0 || operation.QuestionIndex > len(operation.Request.Questions) {
		return ErrInvalidOperation
	}
	if operation.Answers == nil || (operation.LastCallbackID != "" && !saneText(operation.LastCallbackID, 256)) {
		return ErrInvalidOperation
	}
	if operation.Request.Kind == runtimeprotocol.InteractionQuestion {
		if len(operation.Answers) != operation.QuestionIndex {
			return ErrInvalidOperation
		}
		for index := 0; index < operation.QuestionIndex; index++ {
			question := operation.Request.Questions[index]
			answers, found := operation.Answers[question.ID]
			if !found || len(answers) != 1 || (!question.IsOther && !hasQuestionOption(question, answers[0])) {
				return ErrInvalidOperation
			}
		}
	} else if operation.QuestionIndex != 0 || len(operation.Answers) != 0 {
		return ErrInvalidOperation
	}
	switch operation.Phase {
	case PhasePrepared, PhaseSendUnknown:
		if operation.CarrierMessageID != 0 || operation.Response != nil {
			return ErrInvalidOperation
		}
	case PhaseWaiting:
		if operation.CarrierMessageID <= 0 || operation.Response != nil {
			return ErrInvalidOperation
		}
	case PhaseWaitingText:
		if operation.CarrierMessageID <= 0 || operation.Response != nil || operation.Request.Kind != runtimeprotocol.InteractionQuestion ||
			operation.QuestionIndex >= len(operation.Request.Questions) || !operation.Request.Questions[operation.QuestionIndex].IsOther ||
			operation.SecretSourceMessageID != 0 || operation.SecretResponse {
			return ErrInvalidOperation
		}
	case PhaseSecretDeletionUnknown:
		if operation.CarrierMessageID <= 0 || operation.Response != nil || operation.Request.Kind != runtimeprotocol.InteractionQuestion ||
			operation.QuestionIndex >= len(operation.Request.Questions) || !operation.Request.Questions[operation.QuestionIndex].IsOther ||
			!operation.Request.Questions[operation.QuestionIndex].IsSecret || operation.SecretSourceMessageID <= 0 || operation.SecretResponse {
			return ErrInvalidOperation
		}
	case PhaseResponseReady:
		if operation.CarrierMessageID <= 0 || operation.Response == nil ||
			runtimeprotocol.ValidateResponse(operation.Request, *operation.Response, runtimeprotocol.Limits{}) != nil {
			return ErrInvalidOperation
		}
	case PhaseProviderResponseUnknown, PhaseProviderResponseConfirmed:
		if operation.CarrierMessageID <= 0 || (operation.SecretResponse == (operation.Response != nil)) {
			return ErrInvalidOperation
		}
		if operation.Response != nil && runtimeprotocol.ValidateResponse(operation.Request, *operation.Response, runtimeprotocol.Limits{}) != nil {
			return ErrInvalidOperation
		}
		if operation.SecretResponse && (operation.SecretSourceMessageID <= 0 || operation.Resolution != "secret_answered") {
			return ErrInvalidOperation
		}
	default:
		return ErrInvalidOperation
	}
	return nil
}

func hasQuestionOption(question runtimeprotocol.Question, answer string) bool {
	for _, option := range question.Options {
		if option.Label == answer {
			return true
		}
	}
	return false
}

func validTransition(current, next Phase) bool {
	switch current {
	case PhasePrepared:
		return next == PhaseSendUnknown
	case PhaseSendUnknown:
		return next == PhaseWaiting
	case PhaseWaiting:
		return next == PhaseWaiting || next == PhaseWaitingText || next == PhaseResponseReady
	case PhaseWaitingText:
		return next == PhaseResponseReady || next == PhaseSecretDeletionUnknown
	case PhaseSecretDeletionUnknown:
		return next == PhaseProviderResponseUnknown
	case PhaseResponseReady:
		return next == PhaseProviderResponseUnknown
	case PhaseProviderResponseUnknown:
		return next == PhaseProviderResponseConfirmed
	default:
		return false
	}
}

func pendingTextPhase(phase Phase) bool {
	return phase == PhaseWaitingText || phase == PhaseSecretDeletionUnknown
}

func hasOtherPendingText(operations map[string]Operation, id string) bool {
	for candidateID, operation := range operations {
		if candidateID != id && pendingTextPhase(operation.Phase) {
			return true
		}
	}
	return false
}

func pendingTextOperation(operations map[string]Operation, conversationID int64) (Operation, bool, error) {
	var matched Operation
	found := false
	for _, operation := range operations {
		if operation.ConversationID != conversationID || !pendingTextPhase(operation.Phase) {
			continue
		}
		if found {
			return Operation{}, false, ErrInvalidOperation
		}
		matched = cloneOperation(operation)
		found = true
	}
	return matched, found, nil
}

func pruneConfirmed(operations map[string]Operation) bool {
	changed := false
	for id, operation := range operations {
		if operation.Phase == PhaseProviderResponseConfirmed {
			delete(operations, id)
			changed = true
		}
	}
	return changed
}

func sameIdentity(current, next Operation) bool {
	return current.ID == next.ID && current.SessionID == next.SessionID && current.MessageID == next.MessageID &&
		current.ProviderRequestID == next.ProviderRequestID && current.ConversationID == next.ConversationID &&
		current.CreatedAt.Equal(next.CreatedAt) && reflect.DeepEqual(current.Request, next.Request)
}

func cloneOperation(operation Operation) Operation {
	clone := operation
	clone.Request = cloneRequest(operation.Request)
	clone.Answers = cloneAnswers(operation.Answers)
	if operation.Response != nil {
		response := *operation.Response
		response.Answers = cloneAnswers(operation.Response.Answers)
		clone.Response = &response
	}
	return clone
}

func cloneRequest(request runtimeprotocol.InteractionRequest) runtimeprotocol.InteractionRequest {
	clone := request
	if request.Decisions != nil {
		clone.Decisions = append([]runtimeprotocol.ApprovalDecision(nil), request.Decisions...)
	}
	if request.Questions != nil {
		clone.Questions = make([]runtimeprotocol.Question, len(request.Questions))
		for index, question := range request.Questions {
			clone.Questions[index] = question
			if question.Options != nil {
				clone.Questions[index].Options = append([]runtimeprotocol.Option(nil), question.Options...)
			}
		}
	}
	return clone
}

func cloneAnswers(answers map[string][]string) map[string][]string {
	if answers == nil {
		return nil
	}
	clone := make(map[string][]string, len(answers))
	for id, values := range answers {
		clone[id] = append([]string(nil), values...)
	}
	return clone
}

func cloneState(state fileState) fileState {
	clone := fileState{Version: state.Version, Operations: make(map[string]Operation, len(state.Operations))}
	for id, operation := range state.Operations {
		clone.Operations[id] = cloneOperation(operation)
	}
	return clone
}

func saneText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func writeState(path string, state fileState, syncDirectory func(string) error) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxStoreBytes {
		return errors.New("provider interaction store exceeds size limit")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".provider-interactions-")
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
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync provider interaction directory: %w", err)
	}
	return nil
}

func syncStoreDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("provider interaction store contains trailing JSON")
		}
		return fmt.Errorf("decode provider interaction store trailing data: %w", err)
	}
	return nil
}

var _ Store = (*FileStore)(nil)
var _ Store = (*MemoryStore)(nil)
