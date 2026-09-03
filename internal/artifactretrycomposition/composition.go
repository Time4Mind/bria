// Package artifactretrycomposition owns the durable manual decision between
// an unconfirmed artifact delivery and its explicit retry callback.
package artifactretrycomposition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"bria/internal/artifactdelivery"
	"bria/internal/artifactproduction"
	"bria/internal/domain"
	"bria/internal/turnprocessing"
)

var (
	ErrInvalidConfiguration = errors.New("artifact retry composition configuration is invalid")
	ErrInvalidObservation   = errors.New("artifact retry final observation is invalid")
	ErrStaleDecision        = errors.New("artifact retry decision is stale or already used")
)

const retrySlot = uint16(1)

type Delivery interface {
	DeliverFinal(context.Context, turnprocessing.FinalObservation) (artifactproduction.Result, error)
	Retry(context.Context, string) (artifactproduction.Result, error)
}
type claimedRecovery interface {
	RecoverClaimedResults(context.Context) ([]artifactproduction.RecoveredRetry, error)
}

type Publisher interface {
	PublishArtifactRetry(context.Context, Notice) error
}

type Binding struct {
	PresentationID   domain.SessionID `json:"presentation_id"`
	SessionID        domain.SessionID `json:"session_id"`
	MessageID        string           `json:"message_id"`
	FinalOperationID string           `json:"final_operation_id"`
	Generation       uint64           `json:"generation"`
	Slot             uint16           `json:"slot"`
	ExpiresAt        time.Time        `json:"expires_at"`
}

type Notice struct {
	OperationID string
	Sequence    uint64
	Binding     Binding
	Summary     artifactdelivery.Summary
}

type RetryOutcome struct {
	Summary artifactdelivery.Summary
	Next    *Binding
}

type Composition struct {
	mu        sync.Mutex
	delivery  Delivery
	publisher Publisher
	store     *fileStore
	now       func() time.Time
}

func Open(path string, delivery Delivery, now func() time.Time) (*Composition, error) {
	if delivery == nil {
		return nil, ErrInvalidConfiguration
	}
	if now == nil {
		now = time.Now
	}
	store, err := openFileStore(path)
	if err != nil {
		return nil, err
	}
	composition := &Composition{delivery: delivery, store: store, now: now}
	if recovery, ok := delivery.(claimedRecovery); ok {
		entries, err := recovery.RecoverClaimedResults(context.Background())
		if err != nil {
			return nil, err
		}
		if err := composition.reconcile(entries); err != nil {
			return nil, err
		}
	}
	return composition, nil
}

// BindPublisher completes late wiring after Telegram flow construction. It is
// deliberately one-shot so the final processor cannot switch destinations.
func (composition *Composition) BindPublisher(publisher Publisher) error {
	if composition == nil || publisher == nil {
		return ErrInvalidConfiguration
	}
	composition.mu.Lock()
	if composition.publisher != nil {
		composition.mu.Unlock()
		return ErrInvalidConfiguration
	}
	composition.publisher = publisher
	composition.mu.Unlock()
	return composition.publishPending(context.Background())
}

func (composition *Composition) ProcessFinal(ctx context.Context, final turnprocessing.FinalObservation) error {
	if composition == nil || ctx == nil || !validFinal(final) {
		return ErrInvalidObservation
	}
	composition.mu.Lock()
	if existing, found := composition.store.state.Records[final.OperationID]; found {
		if existing.Binding.SessionID != final.SessionID || existing.Binding.MessageID != final.MessageID {
			composition.mu.Unlock()
			return ErrInvalidObservation
		}
		shouldPublish := existing.State == stateIssued && !existing.Published
		composition.mu.Unlock()
		if shouldPublish {
			return composition.publish(final.OperationID, ctx)
		}
		return nil
	}
	result, err := composition.delivery.DeliverFinal(ctx, final)
	if err != nil {
		composition.mu.Unlock()
		return err
	}
	if result.Summary.FinalID != final.OperationID {
		composition.mu.Unlock()
		return ErrInvalidObservation
	}
	record := retryRecord{
		Binding: Binding{SessionID: final.SessionID, MessageID: final.MessageID, FinalOperationID: final.OperationID},
		Summary: result.Summary, State: stateComplete,
	}
	if result.Retry != nil {
		if composition.publisher == nil || !validPendingResult(result, composition.now()) {
			composition.mu.Unlock()
			return ErrInvalidConfiguration
		}
		record.Binding.Generation, record.Binding.Slot = 1, retrySlot
		record.Binding.PresentationID = presentationID(record.Binding)
		record.Descriptor, record.Binding.ExpiresAt, record.State = result.Retry.Token, result.Retry.ExpiresAt.UTC(), stateIssued
	}
	if err := composition.store.put(record); err != nil {
		composition.mu.Unlock()
		return err
	}
	composition.mu.Unlock()
	if result.Retry == nil {
		return nil
	}
	return composition.publish(final.OperationID, ctx)
}

func (composition *Composition) Retry(ctx context.Context, callbackOperationID string, binding Binding) (RetryOutcome, error) {
	if composition == nil || ctx == nil || callbackOperationID == "" || !validBinding(binding) {
		return RetryOutcome{}, ErrStaleDecision
	}
	composition.mu.Lock()
	record, found := composition.store.state.Records[binding.FinalOperationID]
	if found && record.PriorBinding != nil && *record.PriorBinding == binding && record.PriorClaimID == callbackOperationID {
		outcome := RetryOutcome{Summary: record.Summary}
		if record.State == stateIssued {
			next := record.Binding
			outcome.Next = &next
		}
		composition.mu.Unlock()
		return outcome, nil
	}
	if !found || record.State != stateIssued || record.Binding != binding || !record.Binding.ExpiresAt.After(composition.now().UTC()) {
		composition.mu.Unlock()
		return RetryOutcome{}, ErrStaleDecision
	}
	record.State, record.ClaimOperationID, record.PriorBinding, record.PriorClaimID = stateExecuting, callbackOperationID, nil, ""
	if err := composition.store.put(record); err != nil {
		composition.mu.Unlock()
		return RetryOutcome{}, err
	}
	descriptor := record.Descriptor
	composition.mu.Unlock()

	result, err := composition.delivery.Retry(ctx, descriptor)
	if err != nil {
		return RetryOutcome{}, err
	}
	if result.Summary.FinalID != binding.FinalOperationID {
		return RetryOutcome{}, ErrStaleDecision
	}
	composition.mu.Lock()
	defer composition.mu.Unlock()
	current := composition.store.state.Records[binding.FinalOperationID]
	if current.State != stateExecuting || current.Binding != binding || current.ClaimOperationID != callbackOperationID {
		return RetryOutcome{}, ErrStaleDecision
	}
	current.Summary = result.Summary
	if result.Retry == nil {
		current.State, current.Descriptor, current.ClaimOperationID, current.PriorBinding, current.PriorClaimID = stateComplete, "", "", nil, ""
		current.Binding.ExpiresAt = time.Time{}
		if err := composition.store.put(current); err != nil {
			return RetryOutcome{}, err
		}
		return RetryOutcome{Summary: result.Summary}, nil
	}
	if !validPendingResult(result, composition.now()) || binding.Generation == ^uint64(0) {
		return RetryOutcome{}, ErrInvalidConfiguration
	}
	current.Binding.Generation++
	current.Binding.PresentationID = presentationID(current.Binding)
	current.Descriptor, current.Binding.ExpiresAt = result.Retry.Token, result.Retry.ExpiresAt.UTC()
	current.State, current.ClaimOperationID, current.Published = stateIssued, "", true
	if err := composition.store.put(current); err != nil {
		return RetryOutcome{}, err
	}
	next := current.Binding
	return RetryOutcome{Summary: result.Summary, Next: &next}, nil
}

func (composition *Composition) reconcile(entries []artifactproduction.RecoveredRetry) error {
	for _, entry := range entries {
		record, found := composition.store.state.Records[entry.FinalID]
		if !found || record.State != stateExecuting {
			continue
		}
		prior, claimID := record.Binding, record.ClaimOperationID
		record.Summary, record.ClaimOperationID = entry.Summary, ""
		record.PriorBinding, record.PriorClaimID = &prior, claimID
		if entry.Retry == nil {
			if record.Binding.Generation == ^uint64(0) {
				return ErrInvalidConfiguration
			}
			record.Binding.Generation++
			record.State, record.Descriptor, record.Binding.ExpiresAt = stateComplete, "", time.Time{}
		} else {
			if !validPendingResult(artifactproduction.Result{Summary: entry.Summary, Retry: entry.Retry}, composition.now()) || record.Binding.Generation == ^uint64(0) {
				return ErrInvalidConfiguration
			}
			record.Binding.Generation++
			record.Binding.ExpiresAt = entry.Retry.ExpiresAt.UTC()
			record.Binding.PresentationID = presentationID(record.Binding)
			record.Descriptor, record.State = entry.Retry.Token, stateIssued
		}
		record.Published = true
		if err := composition.store.put(record); err != nil {
			return err
		}
	}
	return nil
}

func (composition *Composition) publishPending(ctx context.Context) error {
	composition.mu.Lock()
	ids := make([]string, 0)
	for id, record := range composition.store.state.Records {
		if record.State == stateIssued && !record.Published {
			ids = append(ids, id)
		}
	}
	composition.mu.Unlock()
	for _, id := range ids {
		if err := composition.publish(id, ctx); err != nil {
			return err
		}
	}
	return nil
}

func (composition *Composition) publish(finalID string, ctx context.Context) error {
	composition.mu.Lock()
	record, found := composition.store.state.Records[finalID]
	publisher := composition.publisher
	if !found || record.State != stateIssued || record.Published || publisher == nil || !record.Binding.ExpiresAt.After(composition.now().UTC()) {
		composition.mu.Unlock()
		return ErrInvalidConfiguration
	}
	sequence, err := messageSequence(record.Binding.MessageID)
	composition.mu.Unlock()
	if err != nil {
		return err
	}
	if err := publisher.PublishArtifactRetry(ctx, Notice{OperationID: notificationOperationID(record.Binding), Sequence: sequence, Binding: record.Binding, Summary: record.Summary}); err != nil {
		return err
	}
	composition.mu.Lock()
	defer composition.mu.Unlock()
	current := composition.store.state.Records[finalID]
	if current.State != stateIssued || current.Binding != record.Binding {
		return ErrStaleDecision
	}
	current.Published = true
	return composition.store.put(current)
}

func validFinal(final turnprocessing.FinalObservation) bool {
	return final.SessionID != "" && final.MessageID != "" && final.OperationID == final.MessageID+":final"
}

func validPendingResult(result artifactproduction.Result, now time.Time) bool {
	return result.Retry != nil && result.Retry.Token != "" && strings.TrimSpace(result.Retry.Token) == result.Retry.Token &&
		result.Retry.ExpiresAt.Nanosecond() == 0 && result.Retry.ExpiresAt.After(now.UTC()) &&
		result.Summary.Total > 0 && result.Summary.Confirmed >= 0 && result.Summary.Unconfirmed > 0 &&
		result.Summary.Confirmed+result.Summary.Unconfirmed == result.Summary.Total && result.Summary.NeedsExplicitRetry
}

func validBinding(binding Binding) bool {
	return binding.PresentationID != "" && binding.SessionID != "" && binding.MessageID != "" &&
		binding.FinalOperationID == binding.MessageID+":final" && binding.Generation > 0 && binding.Slot == retrySlot &&
		binding.ExpiresAt.Unix() > 0 && binding.ExpiresAt.Nanosecond() == 0
}

func messageSequence(messageID string) (uint64, error) {
	value, ok := strings.CutPrefix(messageID, "telegram-update:")
	sequence, err := strconv.ParseUint(value, 10, 64)
	if !ok || err != nil || sequence == 0 || strconv.FormatUint(sequence, 10) != value {
		return 0, ErrInvalidObservation
	}
	return sequence, nil
}

func notificationOperationID(binding Binding) string {
	digest := sha256.Sum256([]byte(binding.FinalOperationID + "\x00" + strconv.FormatUint(binding.Generation, 10) + "\x00" + strconv.FormatUint(uint64(binding.Slot), 10)))
	return "artifact-retry:" + hex.EncodeToString(digest[:16])
}

var _ turnprocessing.FinalProcessor = (*Composition)(nil)
