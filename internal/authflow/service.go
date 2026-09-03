package authflow

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Option func(*Service)

func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.clock = clock
		}
	}
}

type Service struct {
	ownerID        int64
	store          Store
	authenticators map[Provider]Authenticator
	deleter        TelegramDeleter
	clock          func() time.Time
	mu             sync.Mutex
}

func (service *Service) Supports(provider Provider) bool {
	if service == nil {
		return false
	}
	_, ok := service.authenticators[provider]
	return ok
}

func NewService(ownerID int64, store Store, authenticators map[Provider]Authenticator, deleter TelegramDeleter, options ...Option) (*Service, error) {
	if ownerID <= 0 || store == nil || deleter == nil {
		return nil, ErrInvalidRequest
	}
	providers := make(map[Provider]Authenticator, len(authenticators))
	for provider, authenticator := range authenticators {
		if !provider.valid() || authenticator == nil {
			return nil, ErrInvalidRequest
		}
		providers[provider] = authenticator
	}
	if len(providers) == 0 {
		return nil, ErrInvalidRequest
	}
	service := &Service{ownerID: ownerID, store: store, authenticators: providers, deleter: deleter, clock: time.Now}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service, nil
}

func (service *Service) Start(ctx context.Context, request StartRequest) (StartResult, error) {
	if err := service.validateStart(request); err != nil {
		return StartResult{}, err
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	if current, exists, err := service.store.Load(ctx, request.OperationID); err != nil {
		return StartResult{}, ErrStateUnavailable
	} else if exists {
		if !sameStartBinding(current, request) {
			return StartResult{}, ErrOperationConflict
		}
		return service.replayStart(ctx, request, current)
	}

	now := service.clock().UTC()
	record, created, err := service.store.Create(ctx, Record{
		OperationID:   request.OperationID,
		OwnerID:       request.ActorID,
		PrivateChatID: request.ChatID,
		ComputerID:    normalized(request.ComputerID),
		Provider:      request.Provider,
		Status:        StatusStarting,
		Deletion:      DeletionNotRequired,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		return StartResult{}, ErrStateUnavailable
	}
	if !created {
		if !sameStartBinding(record, request) {
			return StartResult{}, ErrOperationConflict
		}
		return service.replayStart(ctx, request, record)
	}
	return service.begin(ctx, request, record, false)
}

func (service *Service) replayStart(ctx context.Context, request StartRequest, record Record) (StartResult, error) {
	if record.Status == StatusStarting {
		return service.begin(ctx, request, record, true)
	}
	if record.Status == StatusAwaitingAction && !service.clock().UTC().Before(record.ExpiresAt) {
		record.Status = StatusExpired
		record.UpdatedAt = service.clock().UTC()
		persisted, err := service.replace(ctx, record)
		if err != nil {
			return StartResult{}, err
		}
		record = persisted
	}
	return startResult(record, true), statusError(record.Status)
}

func (service *Service) begin(ctx context.Context, request StartRequest, record Record, replayed bool) (StartResult, error) {
	authenticator := service.authenticators[request.Provider]
	begin, providerErr := authenticator.Begin(ctx, BeginRequest{
		OperationID: request.OperationID, OwnerID: request.ActorID,
		PrivateChatID: request.ChatID, ComputerID: normalized(request.ComputerID), Provider: request.Provider,
	})
	if providerErr != nil && !errors.Is(providerErr, ErrProviderRejected) {
		return startResult(record, replayed), ErrAuthorizationUnconfirmed
	}
	if providerErr != nil || normalized(begin.ChallengeReference) == "" || normalized(begin.Instruction) == "" || begin.ExpiresAt.IsZero() {
		if providerErr == nil {
			return startResult(record, replayed), ErrAuthorizationUnconfirmed
		}
		record.Status = StatusRejected
		record.UpdatedAt = service.clock().UTC()
		persisted, err := service.replace(ctx, record)
		if err != nil {
			return StartResult{}, err
		}
		record = persisted
		return startResult(record, replayed), ErrAuthorizationRejected
	}

	record.ChallengeReference = normalized(begin.ChallengeReference)
	record.ExpiresAt = begin.ExpiresAt.UTC()
	record.Status = StatusAwaitingAction
	record.UpdatedAt = service.clock().UTC()
	persisted, err := service.replace(ctx, record)
	if err != nil {
		return StartResult{}, err
	}
	record = persisted
	result := startResult(record, replayed)
	result.Instruction = begin.Instruction
	return result, nil
}

func (service *Service) Submit(ctx context.Context, request SubmitRequest) (result SubmitResult, resultErr error) {
	if request.ChatID <= 0 || request.MessageID <= 0 {
		request.Secret.Destroy()
		return SubmitResult{}, ErrInvalidRequest
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	defer request.Secret.Destroy()

	if request.ActorID != service.ownerID || request.ConversationKind != "private" {
		// Use the same safe placeholder whether the operation exists or not so
		// an unauthorized caller cannot enumerate authorization operations.
		result.Status = StatusAwaitingAction
		result.Deletion = DeletionConfirmed
		if err := service.deleter.DeleteMessage(context.WithoutCancel(ctx), request.ChatID, request.MessageID); err != nil {
			result.Deletion = DeletionUnconfirmed
			return result, errors.Join(ErrUnauthorized, ErrDeletionUnconfirmed)
		}
		return result, ErrUnauthorized
	}

	current, exists, loadErr := service.store.Load(ctx, request.OperationID)
	confirmedReplay := loadErr == nil && exists && sameSubmission(current, request) && current.Deletion == DeletionConfirmed
	if !confirmedReplay {
		defer func() {
			deletion := DeletionConfirmed
			if err := service.deleter.DeleteMessage(context.WithoutCancel(ctx), request.ChatID, request.MessageID); err != nil {
				deletion = DeletionUnconfirmed
				resultErr = errors.Join(resultErr, ErrDeletionUnconfirmed)
			}
			result.Deletion = deletion
			if result.OperationID != "" && sameSubmission(current, request) {
				current.Deletion = deletion
				current.UpdatedAt = service.clock().UTC()
				persisted, err := service.replace(context.WithoutCancel(ctx), current)
				if err != nil {
					resultErr = errors.Join(resultErr, ErrStateUnavailable)
				} else {
					current = persisted
				}
			}
		}()
	}
	if loadErr != nil {
		return SubmitResult{}, ErrStateUnavailable
	}
	if !exists {
		return SubmitResult{}, ErrOperationNotFound
	}
	result = submitResult(current, false)
	if !validSubmitRequest(request) {
		return result, ErrInvalidRequest
	}
	if !sameOperationBinding(current, request) {
		return result, ErrOperationConflict
	}
	replayedSubmission := current.SubmissionOperationID != ""
	if replayedSubmission {
		if !sameSubmission(current, request) {
			return result, ErrOperationConflict
		}
		result = submitResult(current, true)
		if terminal(current.Status) {
			return result, statusError(current.Status)
		}
		// A crash may leave the operation completing. The stable submission
		// operation ID makes retrying safe for a conforming provider adapter.
	} else {
		if current.Status != StatusAwaitingAction && current.Status != StatusExpired {
			return result, statusError(current.Status)
		}
		current.SubmissionOperationID = request.SubmissionOperationID
		current.SecretMessageReference = SecretMessageReference{ChatID: request.ChatID, MessageID: request.MessageID}
		current.Deletion = DeletionPending
		if current.Status == StatusExpired || !service.clock().UTC().Before(current.ExpiresAt) {
			current.Status = StatusExpired
			current.UpdatedAt = service.clock().UTC()
			persisted, err := service.replace(ctx, current)
			if err != nil {
				return result, err
			}
			current = persisted
			return submitResult(current, false), ErrChallengeExpired
		}
		current.Status = StatusCompleting
		current.UpdatedAt = service.clock().UTC()
		persisted, err := service.replace(ctx, current)
		if err != nil {
			return result, err
		}
		current = persisted
	}

	authenticator, ok := service.authenticators[current.Provider]
	if !ok {
		return submitResult(current, false), ErrProviderUnavailable
	}
	providerErr := authenticator.Complete(ctx, CompleteRequest{
		OperationID: current.OperationID, SubmissionOperationID: current.SubmissionOperationID,
		ComputerID: current.ComputerID, Provider: current.Provider,
		ChallengeReference: current.ChallengeReference, Secret: request.Secret,
	})
	if providerErr != nil && !errors.Is(providerErr, ErrProviderRejected) {
		return submitResult(current, replayedSubmission), ErrAuthorizationUnconfirmed
	}
	if providerErr != nil {
		current.Status = StatusRejected
	} else {
		current.Status = StatusAuthenticated
	}
	current.UpdatedAt = service.clock().UTC()
	persisted, err := service.replace(ctx, current)
	if err != nil {
		return submitResult(current, false), err
	}
	current = persisted
	result = submitResult(current, replayedSubmission)
	if providerErr != nil {
		return result, ErrAuthorizationRejected
	}
	return result, nil
}

// Pending restores every non-terminal operation for the exact owner-private
// conversation. Callers must treat zero as ordinary input, one as a possible
// secret binding, and more than one as ambiguous and fail closed.
func (service *Service) Pending(ctx context.Context, request PendingRequest) ([]PendingOperation, error) {
	if service == nil || request.ActorID != service.ownerID || request.ChatID <= 0 || request.ConversationKind != "private" {
		return nil, ErrUnauthorized
	}
	store, ok := service.store.(PendingStore)
	if !ok {
		return nil, ErrStateUnavailable
	}
	records, err := store.ListPending(ctx, request.ActorID, request.ChatID)
	if err != nil {
		return nil, ErrStateUnavailable
	}
	result := make([]PendingOperation, 0, len(records))
	for _, record := range records {
		if record.OwnerID != request.ActorID || record.PrivateChatID != request.ChatID || terminal(record.Status) {
			return nil, ErrStateUnavailable
		}
		result = append(result, PendingOperation{
			OperationID: record.OperationID, ComputerID: record.ComputerID, Provider: record.Provider,
			ChallengeReference: record.ChallengeReference, Status: record.Status,
		})
	}
	return result, nil
}

// ConsumeMessage intercepts a previously bound Telegram source message before
// any ordinary-input route. It never receives the message body and never
// repeats provider authorization; it only confirms or retries deletion.
func (service *Service) ConsumeMessage(ctx context.Context, request MessageRequest) (MessageResult, error) {
	if service == nil || ctx == nil || request.ActorID != service.ownerID || request.ChatID <= 0 ||
		request.MessageID <= 0 || request.ConversationKind != "private" {
		return MessageResult{}, ErrUnauthorized
	}
	store, ok := service.store.(BoundMessageStore)
	if !ok {
		return MessageResult{}, ErrStateUnavailable
	}
	deletions, ok := service.store.(DeletionIntentStore)
	if !ok {
		return MessageResult{}, ErrStateUnavailable
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	record, recordFound, err := store.FindSubmissionByMessage(ctx, request.ActorID, request.ChatID, request.MessageID)
	if err != nil {
		return MessageResult{}, ErrStateUnavailable
	}
	intent, intentFound, err := store.FindDeletionIntentByMessage(ctx, request.ActorID, request.ChatID, request.MessageID)
	if err != nil || recordFound && intentFound {
		return MessageResult{}, ErrStateUnavailable
	}
	if !recordFound && !intentFound {
		return MessageResult{}, nil
	}
	if recordFound {
		result := MessageResult{Bound: true, Provider: record.Provider, Status: record.Status, Deletion: record.Deletion}
		if record.Deletion == DeletionConfirmed {
			return result, nil
		}
		if err := service.deleter.DeleteMessage(context.WithoutCancel(ctx), request.ChatID, request.MessageID); err != nil {
			record.Deletion = DeletionUnconfirmed
			record.UpdatedAt = service.clock().UTC()
			_, _ = service.replace(context.WithoutCancel(ctx), record)
			result.Deletion = DeletionUnconfirmed
			return result, ErrDeletionUnconfirmed
		}
		record.Deletion = DeletionConfirmed
		record.UpdatedAt = service.clock().UTC()
		if _, err := service.replace(context.WithoutCancel(ctx), record); err != nil {
			result.Deletion = DeletionUnconfirmed
			return result, ErrStateUnavailable
		}
		result.Deletion = DeletionConfirmed
		return result, nil
	}

	result := MessageResult{Bound: true, Deletion: intent.Deletion}
	if intent.Deletion == DeletionConfirmed {
		return result, nil
	}
	if err := service.deleter.DeleteMessage(context.WithoutCancel(ctx), request.ChatID, request.MessageID); err != nil {
		intent.Deletion = DeletionUnconfirmed
		_, _ = deletions.SaveDeletionIntent(context.WithoutCancel(ctx), intent)
		result.Deletion = DeletionUnconfirmed
		return result, ErrDeletionUnconfirmed
	}
	intent.Deletion = DeletionConfirmed
	if _, err := deletions.SaveDeletionIntent(context.WithoutCancel(ctx), intent); err != nil {
		result.Deletion = DeletionUnconfirmed
		return result, ErrStateUnavailable
	}
	result.Deletion = DeletionConfirmed
	return result, nil
}

// Discard deletes a message that cannot be safely matched to exactly one
// pending operation. It never accepts or persists the message body.
func (service *Service) Discard(ctx context.Context, request DiscardRequest) (DiscardResult, error) {
	result := DiscardResult{Deletion: DeletionConfirmed}
	if service == nil || normalized(request.OperationID) == "" || request.ChatID <= 0 || request.MessageID <= 0 {
		return DiscardResult{Deletion: DeletionUnconfirmed}, ErrInvalidRequest
	}
	store, ok := service.store.(DeletionIntentStore)
	if !ok {
		return DiscardResult{Deletion: DeletionUnconfirmed}, ErrStateUnavailable
	}
	intent := DeletionIntent{OperationID: request.OperationID, ActorID: request.ActorID, ChatID: request.ChatID, MessageID: request.MessageID, Deletion: DeletionPending}
	current, found, err := store.LoadDeletionIntent(ctx, request.OperationID)
	if err != nil {
		return DiscardResult{Deletion: DeletionUnconfirmed}, ErrStateUnavailable
	}
	if found {
		if !sameDeletionBinding(current, intent) {
			return DiscardResult{Deletion: DeletionUnconfirmed}, ErrOperationConflict
		}
		if current.Deletion == DeletionConfirmed {
			return DiscardResult{Deletion: DeletionConfirmed}, nil
		}
	} else if _, err := store.SaveDeletionIntent(ctx, intent); err != nil {
		return DiscardResult{Deletion: DeletionUnconfirmed}, ErrStateUnavailable
	}
	if err := service.deleter.DeleteMessage(context.WithoutCancel(ctx), request.ChatID, request.MessageID); err != nil {
		result.Deletion = DeletionUnconfirmed
		intent.Deletion = DeletionUnconfirmed
		if _, persistErr := store.SaveDeletionIntent(context.WithoutCancel(ctx), intent); persistErr != nil {
			return result, errors.Join(ErrDeletionUnconfirmed, ErrStateUnavailable)
		}
		if request.ActorID != service.ownerID || request.ConversationKind != "private" {
			return result, errors.Join(ErrUnauthorized, ErrDeletionUnconfirmed)
		}
		return result, ErrDeletionUnconfirmed
	}
	intent.Deletion = DeletionConfirmed
	if _, err := store.SaveDeletionIntent(context.WithoutCancel(ctx), intent); err != nil {
		return DiscardResult{Deletion: DeletionUnconfirmed}, ErrStateUnavailable
	}
	if request.ActorID != service.ownerID || request.ConversationKind != "private" {
		return result, ErrUnauthorized
	}
	return result, nil
}

func (service *Service) validateStart(request StartRequest) error {
	if request.ActorID != service.ownerID || request.ConversationKind != "private" {
		return ErrUnauthorized
	}
	if normalized(request.OperationID) == "" || request.ChatID <= 0 || normalized(request.ComputerID) == "" || !request.Provider.valid() {
		return ErrInvalidRequest
	}
	if _, ok := service.authenticators[request.Provider]; !ok {
		return ErrProviderUnavailable
	}
	return nil
}

func validSubmitRequest(request SubmitRequest) bool {
	return normalized(request.OperationID) != "" && normalized(request.SubmissionOperationID) != "" &&
		request.ChatID > 0 && request.MessageID > 0 && normalized(request.ComputerID) != "" &&
		request.Provider.valid() && normalized(request.ChallengeReference) != "" && !request.Secret.Empty()
}

func sameStartBinding(record Record, request StartRequest) bool {
	return record.OwnerID == request.ActorID && record.PrivateChatID == request.ChatID &&
		record.ComputerID == normalized(request.ComputerID) && record.Provider == request.Provider
}

func sameOperationBinding(record Record, request SubmitRequest) bool {
	return record.OwnerID == request.ActorID && record.PrivateChatID == request.ChatID &&
		record.ComputerID == normalized(request.ComputerID) && record.Provider == request.Provider &&
		record.ChallengeReference == normalized(request.ChallengeReference)
}

func sameSubmission(record Record, request SubmitRequest) bool {
	return sameOperationBinding(record, request) && record.SubmissionOperationID == request.SubmissionOperationID &&
		record.SecretMessageReference.ChatID == request.ChatID && record.SecretMessageReference.MessageID == request.MessageID
}

func terminal(status Status) bool {
	return status == StatusAuthenticated || status == StatusRejected || status == StatusExpired
}

func statusError(status Status) error {
	switch status {
	case StatusRejected:
		return ErrAuthorizationRejected
	case StatusExpired:
		return ErrChallengeExpired
	default:
		return nil
	}
}

func startResult(record Record, replayed bool) StartResult {
	return StartResult{OperationID: record.OperationID, ChallengeReference: record.ChallengeReference, ExpiresAt: record.ExpiresAt, Status: record.Status, Replayed: replayed}
}

func submitResult(record Record, replayed bool) SubmitResult {
	return SubmitResult{OperationID: record.OperationID, Status: record.Status, Deletion: record.Deletion, Replayed: replayed}
}

func (service *Service) replace(ctx context.Context, next Record) (Record, error) {
	persisted, swapped, err := service.store.CompareAndSwap(ctx, next.OperationID, next.Revision, next)
	if err != nil || !swapped {
		return Record{}, ErrStateUnavailable
	}
	return persisted, nil
}
