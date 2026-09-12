// Package callbackregistry defines callback presentation custody and its in-memory test implementation.
package callbackregistry

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramrecovery"
	"bria/internal/telegramrecovery/statusrecovery"
	"bria/internal/telegramstate"
)

type RecoveryBinding struct {
	OperationID string
	UpdateID    int64
	SessionID   domain.SessionID
	Carrier     telegramstate.Carrier
	Phase       string
}
type AcceptedTurnRecoveryBinding = telegramrecovery.AcceptedTurnBinding
type StatusRecoveryBinding = statusrecovery.Binding
type ArtifactRetryBinding = telegrambridge.ArtifactRetryBinding
type Presentation struct {
	SessionID            domain.SessionID
	Carrier              telegramstate.Carrier
	TokenIDs             []string
	ExpiresAt            time.Time
	InteractionRequestID string
	OutboundOperationID  string
	OutboundUpdateID     int64
	Recovery             *RecoveryBinding
	AcceptedTurnRecovery *AcceptedTurnRecoveryBinding
	StatusRecovery       *StatusRecoveryBinding
	ArtifactRetry        *ArtifactRetryBinding
}
type Claim struct {
	SessionID        domain.SessionID
	Carrier          telegramstate.Carrier
	TokenID          string
	ExpiresAt        time.Time
	UpdateID         int64
	CallbackQueryID  string
	Repeatable       bool
	DurableOperation bool
}
type Outcome string

const (
	Accepted  Outcome = "accepted"
	Recovered Outcome = "recovered"
	Stale     Outcome = "stale"
	Replayed  Outcome = "replayed"
)

const (
	EffectUnknownPhase      = "effect_unknown"
	EffectRetryUnknownPhase = "effect_retry_unknown"
	SendUnknownPhase        = "send_unknown"
)

type ClaimResult struct {
	Outcome               Outcome
	PresentationSessionID domain.SessionID
	InteractionRequestID  string
	OutboundOperationID   string
	OutboundUpdateID      int64
	Recovery              *RecoveryBinding
	AcceptedTurnRecovery  *AcceptedTurnRecoveryBinding
	StatusRecovery        *StatusRecoveryBinding
	ArtifactRetry         *ArtifactRetryBinding
	Retired               bool
}
type Registry interface {
	Replace(context.Context, Presentation) error
	Current(context.Context, domain.SessionID) (Presentation, bool, error)
	Claim(context.Context, Claim) (ClaimResult, error)
	InvalidateCarrier(context.Context, telegramstate.Carrier) error
}

type claimIdentity struct {
	UpdateID        int64
	CallbackQueryID string
}
type memoryPresentation struct {
	presentation Presentation
	available    map[string]struct{}
	claimed      map[string]claimIdentity
}
type Memory struct {
	mu            sync.Mutex
	now           func() time.Time
	presentations map[domain.SessionID]memoryPresentation
	retired       map[string]memoryPresentation
}

func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, presentations: make(map[domain.SessionID]memoryPresentation), retired: make(map[string]memoryPresentation)}
}

func (registry *Memory) Replace(ctx context.Context, presentation Presentation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry == nil || registry.now == nil {
		return errors.New("callback registry is required")
	}
	if err := ValidatePresentation(presentation, registry.now()); err != nil {
		return err
	}
	available := make(map[string]struct{}, len(presentation.TokenIDs))
	for _, tokenID := range presentation.TokenIDs {
		available[tokenID] = struct{}{}
	}
	copyPresentation := clonePresentation(presentation)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if previous, exists := registry.presentations[presentation.SessionID]; exists && reflect.DeepEqual(previous.available, available) {
		previous.presentation.TokenIDs = copyPresentation.TokenIDs
		if reflect.DeepEqual(previous.presentation, copyPresentation) {
			return nil
		}
	}
	for sessionID, candidate := range registry.presentations {
		if candidate.presentation.Carrier != presentation.Carrier {
			continue
		}
		for tokenID := range candidate.available {
			registry.retired[tokenID] = cloneMemoryPresentation(candidate)
		}
		if sessionID != presentation.SessionID {
			delete(registry.presentations, sessionID)
		}
	}
	registry.presentations[presentation.SessionID] = memoryPresentation{presentation: copyPresentation, available: available, claimed: make(map[string]claimIdentity)}
	return nil
}

func (registry *Memory) Current(ctx context.Context, sessionID domain.SessionID) (Presentation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Presentation{}, false, err
	}
	if registry == nil || registry.now == nil || sessionID == "" {
		return Presentation{}, false, errors.New("callback registry and presentation identity are required")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, found := registry.presentations[sessionID]
	if !found || !current.presentation.ExpiresAt.After(registry.now()) {
		return Presentation{}, false, nil
	}
	return clonePresentation(current.presentation), true, nil
}

func (registry *Memory) Claim(ctx context.Context, claim Claim) (ClaimResult, error) {
	if err := ctx.Err(); err != nil {
		return ClaimResult{}, err
	}
	if registry == nil || registry.now == nil {
		return ClaimResult{}, errors.New("callback registry is required")
	}
	if err := ValidateClaim(claim); err != nil {
		return ClaimResult{}, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	var owner domain.SessionID
	var current memoryPresentation
	retired := false
	for sessionID, candidate := range registry.presentations {
		if candidate.presentation.Carrier == claim.Carrier && candidate.presentation.ExpiresAt == claim.ExpiresAt && candidate.presentation.ExpiresAt.After(registry.now()) {
			if _, ok := candidate.available[claim.TokenID]; ok {
				owner, current = sessionID, candidate
				break
			}
		}
	}
	if owner == "" {
		for sessionID, candidate := range registry.presentations {
			if candidate.presentation.Carrier.ChatID != claim.Carrier.ChatID || candidate.presentation.ExpiresAt != claim.ExpiresAt || !candidate.presentation.ExpiresAt.After(registry.now()) {
				continue
			}
			if _, ok := candidate.available[claim.TokenID]; !ok {
				continue
			}
			if owner != "" {
				owner = ""
				break
			}
			owner, current = sessionID, candidate
		}
	}
	if owner == "" && claim.Repeatable {
		if candidate, ok := registry.retired[claim.TokenID]; ok && candidate.presentation.Carrier == claim.Carrier && candidate.presentation.ExpiresAt == claim.ExpiresAt && candidate.presentation.ExpiresAt.After(registry.now()) && repeatable(candidate.presentation) {
			owner, current, retired = candidate.presentation.SessionID, candidate, true
		}
	}
	if owner == "" {
		return ClaimResult{Outcome: Stale}, nil
	}
	if claim.DurableOperation && claim.Repeatable && repeatable(current.presentation) {
		return result(Accepted, owner, current.presentation, retired), nil
	}
	if identity, used := current.claimed[claim.TokenID]; used {
		if identity.UpdateID == claim.UpdateID && identity.CallbackQueryID == claim.CallbackQueryID {
			return result(Recovered, owner, current.presentation, retired), nil
		}
		if claim.Repeatable && repeatable(current.presentation) {
			return result(Accepted, owner, current.presentation, retired), nil
		}
		return result(Replayed, owner, current.presentation, retired), nil
	}
	current.claimed[claim.TokenID] = claimIdentity{UpdateID: claim.UpdateID, CallbackQueryID: claim.CallbackQueryID}
	if retired {
		registry.retired[claim.TokenID] = current
	} else {
		registry.presentations[owner] = current
	}
	return result(Accepted, owner, current.presentation, retired), nil
}

func (registry *Memory) InvalidateCarrier(ctx context.Context, carrier telegramstate.Carrier) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry == nil || carrier.ChatID <= 0 || carrier.MessageID <= 0 {
		return errors.New("callback registry and carrier are required")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for sessionID, candidate := range registry.presentations {
		if candidate.presentation.Carrier == carrier {
			delete(registry.presentations, sessionID)
		}
	}
	for tokenID, candidate := range registry.retired {
		if candidate.presentation.Carrier == carrier {
			delete(registry.retired, tokenID)
		}
	}
	return nil
}

func repeatable(p Presentation) bool {
	return p.InteractionRequestID == "" && p.OutboundOperationID == "" && p.Recovery == nil && p.AcceptedTurnRecovery == nil && p.StatusRecovery == nil && p.ArtifactRetry == nil
}
func result(outcome Outcome, owner domain.SessionID, p Presentation, retired bool) ClaimResult {
	return ClaimResult{Outcome: outcome, PresentationSessionID: owner, InteractionRequestID: p.InteractionRequestID, OutboundOperationID: p.OutboundOperationID, OutboundUpdateID: p.OutboundUpdateID, Recovery: clone(p.Recovery), AcceptedTurnRecovery: clone(p.AcceptedTurnRecovery), StatusRecovery: clone(p.StatusRecovery), ArtifactRetry: clone(p.ArtifactRetry), Retired: retired}
}
func clonePresentation(p Presentation) Presentation {
	p.TokenIDs = append([]string(nil), p.TokenIDs...)
	p.Recovery, p.AcceptedTurnRecovery, p.StatusRecovery, p.ArtifactRetry = clone(p.Recovery), clone(p.AcceptedTurnRecovery), clone(p.StatusRecovery), clone(p.ArtifactRetry)
	return p
}
func cloneMemoryPresentation(value memoryPresentation) memoryPresentation {
	copy := memoryPresentation{presentation: clonePresentation(value.presentation), available: make(map[string]struct{}, len(value.available)), claimed: make(map[string]claimIdentity, len(value.claimed))}
	for tokenID := range value.available {
		copy.available[tokenID] = struct{}{}
	}
	for tokenID, identity := range value.claimed {
		copy.claimed[tokenID] = identity
	}
	return copy
}
func clone[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func ValidatePresentation(p Presentation, now time.Time) error {
	if p.SessionID == "" || p.Carrier.ChatID <= 0 || p.Carrier.MessageID <= 0 || len(p.TokenIDs) == 0 {
		return errors.New("callback presentation identity is required")
	}
	if len(p.InteractionRequestID) > 256 || !utf8.ValidString(p.InteractionRequestID) || !validOutbound(p.OutboundOperationID, p.OutboundUpdateID) {
		return errors.New("callback presentation binding identity is invalid")
	}
	if p.Recovery != nil && !ValidRecovery(p.Recovery) || p.AcceptedTurnRecovery != nil && (!ValidAccepted(p.AcceptedTurnRecovery) || p.AcceptedTurnRecovery.SessionID != p.SessionID) || p.StatusRecovery != nil && !ValidStatus(p.StatusRecovery) || p.ArtifactRetry != nil && (!ValidArtifact(p.ArtifactRetry) || p.SessionID != domain.SessionID(p.ArtifactRetry.PresentationID)) {
		return errors.New("callback presentation special binding is invalid")
	}
	if specialCount(p.InteractionRequestID != "", p.OutboundOperationID != "", p.Recovery != nil, p.AcceptedTurnRecovery != nil, p.StatusRecovery != nil, p.ArtifactRetry != nil) > 1 {
		return errors.New("callback presentation has conflicting bindings")
	}
	if p.ExpiresAt.Nanosecond() != 0 || !p.ExpiresAt.After(now) {
		return errors.New("callback presentation expiry must be a future whole second")
	}
	seen := make(map[string]struct{}, len(p.TokenIDs))
	for _, tokenID := range p.TokenIDs {
		if !validToken(tokenID) {
			return errors.New("callback presentation token identity is invalid")
		}
		if _, duplicate := seen[tokenID]; duplicate {
			return errors.New("callback presentation token identities must be unique")
		}
		seen[tokenID] = struct{}{}
	}
	return nil
}

func ValidateClaim(claim Claim) error {
	if claim.SessionID == "" || claim.Carrier.ChatID <= 0 || claim.Carrier.MessageID <= 0 || !validToken(claim.TokenID) || claim.ExpiresAt.IsZero() || claim.ExpiresAt.Nanosecond() != 0 || claim.UpdateID <= 0 || claim.CallbackQueryID == "" {
		return errors.New("callback claim identity is required")
	}
	return nil
}

func ValidRecovery(binding *RecoveryBinding) bool {
	return binding != nil && binding.OperationID != "" && len(binding.OperationID) <= 256 && utf8.ValidString(binding.OperationID) && binding.UpdateID > 0 && binding.SessionID != "" && binding.Carrier.ChatID > 0 && binding.Carrier.MessageID > 0 && (binding.Phase == EffectUnknownPhase || binding.Phase == EffectRetryUnknownPhase || binding.Phase == SendUnknownPhase)
}
func ValidAccepted(binding *AcceptedTurnRecoveryBinding) bool {
	return telegramrecovery.ValidAcceptedTurnBinding(binding)
}
func ValidStatus(binding *StatusRecoveryBinding) bool {
	return binding != nil && statusrecovery.Valid(*binding)
}
func ValidArtifact(binding *ArtifactRetryBinding) bool {
	return binding != nil && binding.PresentationID != "" && binding.SessionID != "" && binding.PresentationID != binding.SessionID && binding.MessageID != "" && len(binding.MessageID) <= 1024 && binding.FinalOperationID == binding.MessageID+":final" && binding.Generation > 0 && binding.Slot > 0 && binding.ExpiresAt.Unix() > 0 && binding.ExpiresAt.Nanosecond() == 0
}
func validOutbound(operationID string, updateID int64) bool {
	return operationID == "" && updateID == 0 || operationID != "" && len(operationID) <= 256 && utf8.ValidString(operationID) && updateID > 0
}
func validToken(tokenID string) bool {
	return tokenID != "" && len(tokenID) <= 128 && utf8.ValidString(tokenID)
}
func specialCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}
