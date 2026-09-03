package telegrampipeline

import (
	"bria/internal/domain"
	"bria/internal/telegramstate"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	callbackRegistryVersion = 1
	maxCallbackRegistrySize = 16 << 20
	maxCallbackTokenIDBytes = 128
)

type fileCallbackPresentation struct {
	SessionID            domain.SessionID                     `json:"session_id"`
	Carrier              telegramstate.Carrier                `json:"carrier"`
	ExpiresAt            time.Time                            `json:"expires_at"`
	Tokens               map[string]bool                      `json:"tokens"`
	Claims               map[string]fileCallbackClaimIdentity `json:"claims,omitempty"`
	InteractionRequestID string                               `json:"interaction_request_id,omitempty"`
	OutboundOperationID  string                               `json:"outbound_operation_id,omitempty"`
	OutboundUpdateID     int64                                `json:"outbound_update_id,omitempty"`
	Recovery             *CallbackRecoveryBinding             `json:"recovery,omitempty"`
	AcceptedTurnRecovery *AcceptedTurnRecoveryBinding         `json:"accepted_turn_recovery,omitempty"`
	StatusRecovery       *StatusRecoveryBinding               `json:"status_recovery,omitempty"`
	ArtifactRetry        *ArtifactRetryBinding                `json:"artifact_retry,omitempty"`
}
type fileCallbackClaimIdentity struct {
	UpdateID        int64  `json:"update_id"`
	CallbackQueryID string `json:"callback_query_id"`
}
type fileCallbackRegistryState struct {
	Version       int                                           `json:"version"`
	Presentations map[domain.SessionID]fileCallbackPresentation `json:"presentations"`
}

type FileCallbackRegistry struct {
	mu            sync.Mutex
	path          string
	now           func() time.Time
	syncDirectory func(string) error
	state         fileCallbackRegistryState
}

func OpenFileCallbackRegistry(path string, now func() time.Time) (*FileCallbackRegistry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("callback registry path is required")
	}
	if now == nil {
		now = time.Now
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve callback registry path: %w", err)
	}
	registry := &FileCallbackRegistry{
		path:          absolute,
		now:           now,
		syncDirectory: syncCallbackRegistryDirectory,
		state: fileCallbackRegistryState{
			Version:       callbackRegistryVersion,
			Presentations: make(map[domain.SessionID]fileCallbackPresentation),
		},
	}
	info, err := os.Stat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return registry, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat callback registry: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxCallbackRegistrySize {
		return nil, errors.New("callback registry must be a bounded regular file")
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, fmt.Errorf("read callback registry: %w", err)
	}
	if err := json.Unmarshal(data, &registry.state); err != nil {
		return nil, fmt.Errorf("decode callback registry: %w", err)
	}
	if err := validateFileCallbackRegistryState(registry.state); err != nil {
		return nil, fmt.Errorf("validate callback registry: %w", err)
	}
	return registry, nil
}
func (registry *FileCallbackRegistry) Replace(ctx context.Context, presentation CallbackPresentation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry == nil || registry.now == nil {
		return errors.New("callback registry is required")
	}
	if err := validateCallbackPresentation(presentation, registry.now()); err != nil {
		return err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	next := cloneFileCallbackRegistryState(registry.state)
	for sessionID, candidate := range next.Presentations {
		if sessionID != presentation.SessionID && candidate.Carrier == presentation.Carrier {
			delete(next.Presentations, sessionID)
		}
	}
	tokens := make(map[string]bool, len(presentation.TokenIDs))
	for _, tokenID := range presentation.TokenIDs {
		tokens[tokenID] = false
	}
	next.Presentations[presentation.SessionID] = fileCallbackPresentation{
		SessionID:            presentation.SessionID,
		Carrier:              presentation.Carrier,
		ExpiresAt:            presentation.ExpiresAt,
		Tokens:               tokens,
		Claims:               make(map[string]fileCallbackClaimIdentity),
		InteractionRequestID: presentation.InteractionRequestID,
		OutboundOperationID:  presentation.OutboundOperationID,
		OutboundUpdateID:     presentation.OutboundUpdateID,
		Recovery:             cloneCallbackRecoveryBinding(presentation.Recovery),
		AcceptedTurnRecovery: cloneAcceptedTurnRecoveryBinding(presentation.AcceptedTurnRecovery),
		StatusRecovery:       cloneStatusRecoveryBinding(presentation.StatusRecovery),
		ArtifactRetry:        cloneArtifactRetryBinding(presentation.ArtifactRetry),
	}
	if err := writeCallbackRegistryAtomic(registry.path, next, registry.syncDirectory); err != nil {
		return fmt.Errorf("persist callback presentation: %w", err)
	}
	registry.state = next
	return nil
}
func (registry *FileCallbackRegistry) Claim(ctx context.Context, claim CallbackClaim) (CallbackClaimResult, error) {
	if err := ctx.Err(); err != nil {
		return CallbackClaimResult{}, err
	}
	if registry == nil || registry.now == nil {
		return CallbackClaimResult{}, errors.New("callback registry is required")
	}
	if err := validateCallbackClaim(claim); err != nil {
		return CallbackClaimResult{}, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	ownerSessionID, presentation, found := findFileCallbackPresentation(registry.state, claim, registry.now())
	if !found {
		return CallbackClaimResult{Outcome: ClaimStale}, nil
	}
	if presentation.Tokens[claim.TokenID] {
		identity, known := presentation.Claims[claim.TokenID]
		if known && identity.UpdateID == claim.UpdateID && identity.CallbackQueryID == claim.CallbackQueryID {
			return fileCallbackClaimResult(ClaimRecovered, ownerSessionID, presentation), nil
		}
		return fileCallbackClaimResult(ClaimReplayed, ownerSessionID, presentation), nil
	}
	next := cloneFileCallbackRegistryState(registry.state)
	nextPresentation := next.Presentations[ownerSessionID]
	nextPresentation.Tokens[claim.TokenID] = true
	if nextPresentation.Claims == nil {
		nextPresentation.Claims = make(map[string]fileCallbackClaimIdentity)
	}
	nextPresentation.Claims[claim.TokenID] = fileCallbackClaimIdentity{
		UpdateID: claim.UpdateID, CallbackQueryID: claim.CallbackQueryID,
	}
	next.Presentations[ownerSessionID] = nextPresentation
	if err := writeCallbackRegistryAtomic(registry.path, next, registry.syncDirectory); err != nil {
		return CallbackClaimResult{}, fmt.Errorf("persist callback claim: %w", err)
	}
	registry.state = next
	return fileCallbackClaimResult(ClaimAccepted, ownerSessionID, presentation), nil
}
func fileCallbackClaimResult(outcome ClaimOutcome, ownerSessionID domain.SessionID, presentation fileCallbackPresentation) CallbackClaimResult {
	return CallbackClaimResult{
		Outcome: outcome, PresentationSessionID: ownerSessionID,
		InteractionRequestID: presentation.InteractionRequestID,
		OutboundOperationID:  presentation.OutboundOperationID, OutboundUpdateID: presentation.OutboundUpdateID,
		Recovery:             cloneCallbackRecoveryBinding(presentation.Recovery),
		AcceptedTurnRecovery: cloneAcceptedTurnRecoveryBinding(presentation.AcceptedTurnRecovery),
		StatusRecovery:       cloneStatusRecoveryBinding(presentation.StatusRecovery),
		ArtifactRetry:        cloneArtifactRetryBinding(presentation.ArtifactRetry),
	}
}
func (registry *FileCallbackRegistry) InvalidateCarrier(ctx context.Context, carrier telegramstate.Carrier) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry == nil || carrier.ChatID <= 0 || carrier.MessageID <= 0 {
		return errors.New("callback registry and carrier are required")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	next := cloneFileCallbackRegistryState(registry.state)
	changed := false
	for sessionID, presentation := range next.Presentations {
		if presentation.Carrier == carrier {
			delete(next.Presentations, sessionID)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := writeCallbackRegistryAtomic(registry.path, next, registry.syncDirectory); err != nil {
		return fmt.Errorf("persist callback carrier invalidation: %w", err)
	}
	registry.state = next
	return nil
}
func findFileCallbackPresentation(state fileCallbackRegistryState, claim CallbackClaim, now time.Time) (domain.SessionID, fileCallbackPresentation, bool) {
	for sessionID, candidate := range state.Presentations {
		if candidate.Carrier != claim.Carrier || candidate.ExpiresAt != claim.ExpiresAt ||
			!candidate.ExpiresAt.After(now) {
			continue
		}
		if _, ok := candidate.Tokens[claim.TokenID]; !ok {
			continue
		}
		return sessionID, candidate, true
	}
	return "", fileCallbackPresentation{}, false
}
func validateFileCallbackRegistryState(state fileCallbackRegistryState) error {
	if state.Version != callbackRegistryVersion {
		return fmt.Errorf("unsupported callback registry version %d", state.Version)
	}
	if state.Presentations == nil {
		return errors.New("callback registry presentations are required")
	}
	for sessionID, presentation := range state.Presentations {
		if sessionID == "" || presentation.SessionID != sessionID {
			return errors.New("callback presentation session identity is invalid")
		}
		if presentation.Carrier.ChatID <= 0 || presentation.Carrier.MessageID <= 0 {
			return errors.New("callback presentation carrier is invalid")
		}
		if presentation.ExpiresAt.IsZero() || presentation.ExpiresAt.Nanosecond() != 0 {
			return errors.New("callback presentation expiry is invalid")
		}
		if len(presentation.Tokens) == 0 {
			return errors.New("callback presentation tokens are required")
		}
		if len(presentation.InteractionRequestID) > 256 || !utf8.ValidString(presentation.InteractionRequestID) {
			return errors.New("callback interaction request identity is invalid")
		}
		if !validOutboundBinding(presentation.OutboundOperationID, presentation.OutboundUpdateID) {
			return errors.New("callback outbound resolution identity is invalid")
		}
		if presentation.Recovery != nil && !validCallbackRecoveryBinding(presentation.Recovery) {
			return errors.New("callback recovery identity is invalid")
		}
		if presentation.AcceptedTurnRecovery != nil && (!validAcceptedTurnRecoveryBinding(presentation.AcceptedTurnRecovery) || presentation.AcceptedTurnRecovery.SessionID != presentation.SessionID) {
			return errors.New("accepted-turn recovery identity is invalid")
		}
		if presentation.StatusRecovery != nil && !validStatusRecoveryBinding(presentation.StatusRecovery) {
			return errors.New("status recovery identity is invalid")
		}
		if presentation.ArtifactRetry != nil && (!validArtifactRetryBinding(presentation.ArtifactRetry) || presentation.SessionID != domain.SessionID(presentation.ArtifactRetry.PresentationID)) {
			return errors.New("artifact retry identity is invalid")
		}
		if callbackSpecialBindingCount(presentation.InteractionRequestID != "", presentation.OutboundOperationID != "", presentation.Recovery != nil, presentation.AcceptedTurnRecovery != nil, presentation.StatusRecovery != nil, presentation.ArtifactRetry != nil) > 1 {
			return errors.New("callback presentation has conflicting bindings")
		}
		for tokenID := range presentation.Tokens {
			if !validCallbackTokenID(tokenID) {
				return errors.New("callback presentation token identity is invalid")
			}
		}
		for tokenID, identity := range presentation.Claims {
			claimed, exists := presentation.Tokens[tokenID]
			if !exists || !claimed || identity.UpdateID <= 0 || identity.CallbackQueryID == "" {
				return errors.New("callback presentation claim identity is invalid")
			}
		}
	}
	return nil
}
func cloneFileCallbackRegistryState(state fileCallbackRegistryState) fileCallbackRegistryState {
	clone := fileCallbackRegistryState{
		Version:       state.Version,
		Presentations: make(map[domain.SessionID]fileCallbackPresentation, len(state.Presentations)),
	}
	for sessionID, presentation := range state.Presentations {
		copyPresentation := presentation
		copyPresentation.Tokens = make(map[string]bool, len(presentation.Tokens))
		for tokenID, claimed := range presentation.Tokens {
			copyPresentation.Tokens[tokenID] = claimed
		}
		copyPresentation.Claims = make(map[string]fileCallbackClaimIdentity, len(presentation.Claims))
		for tokenID, identity := range presentation.Claims {
			copyPresentation.Claims[tokenID] = identity
		}
		copyPresentation.Recovery = cloneCallbackRecoveryBinding(presentation.Recovery)
		copyPresentation.AcceptedTurnRecovery = cloneAcceptedTurnRecoveryBinding(presentation.AcceptedTurnRecovery)
		copyPresentation.StatusRecovery = cloneStatusRecoveryBinding(presentation.StatusRecovery)
		copyPresentation.ArtifactRetry = cloneArtifactRetryBinding(presentation.ArtifactRetry)
		clone.Presentations[sessionID] = copyPresentation
	}
	return clone
}
func writeCallbackRegistryAtomic(path string, state fileCallbackRegistryState, syncDirectory func(string) error) error {
	if syncDirectory == nil {
		return errors.New("callback registry directory sync is required")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxCallbackRegistrySize {
		return errors.New("callback registry exceeds size limit")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".callback-registry-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	failed := true
	defer func() {
		if failed {
			_ = temporary.Close()
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync callback registry directory: %w", err)
	}
	failed = false
	return nil
}
func syncCallbackRegistryDirectory(directory string) error {
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
func validCallbackTokenID(tokenID string) bool {
	return tokenID != "" && len(tokenID) <= maxCallbackTokenIDBytes && utf8.ValidString(tokenID)
}
func validateCallbackPresentation(presentation CallbackPresentation, now time.Time) error {
	if presentation.SessionID == "" || presentation.Carrier.ChatID <= 0 || presentation.Carrier.MessageID <= 0 {
		return errors.New("callback presentation identity is required")
	}
	if len(presentation.TokenIDs) == 0 {
		return errors.New("callback presentation must contain at least one token")
	}
	if len(presentation.InteractionRequestID) > 256 || !utf8.ValidString(presentation.InteractionRequestID) {
		return errors.New("callback interaction request identity is invalid")
	}
	if !validOutboundBinding(presentation.OutboundOperationID, presentation.OutboundUpdateID) {
		return errors.New("callback outbound resolution identity is invalid")
	}
	if presentation.Recovery != nil && !validCallbackRecoveryBinding(presentation.Recovery) {
		return errors.New("callback recovery identity is invalid")
	}
	if presentation.AcceptedTurnRecovery != nil && (!validAcceptedTurnRecoveryBinding(presentation.AcceptedTurnRecovery) || presentation.AcceptedTurnRecovery.SessionID != presentation.SessionID) {
		return errors.New("accepted-turn recovery identity is invalid")
	}
	if presentation.StatusRecovery != nil && !validStatusRecoveryBinding(presentation.StatusRecovery) {
		return errors.New("status recovery identity is invalid")
	}
	if presentation.ArtifactRetry != nil && (!validArtifactRetryBinding(presentation.ArtifactRetry) || presentation.SessionID != domain.SessionID(presentation.ArtifactRetry.PresentationID)) {
		return errors.New("artifact retry identity is invalid")
	}
	if callbackSpecialBindingCount(presentation.InteractionRequestID != "", presentation.OutboundOperationID != "", presentation.Recovery != nil, presentation.AcceptedTurnRecovery != nil, presentation.StatusRecovery != nil, presentation.ArtifactRetry != nil) > 1 {
		return errors.New("callback presentation has conflicting bindings")
	}
	if presentation.ExpiresAt.Nanosecond() != 0 || !presentation.ExpiresAt.After(now) {
		return errors.New("callback presentation expiry must be a future whole second")
	}
	seen := make(map[string]struct{}, len(presentation.TokenIDs))
	for _, tokenID := range presentation.TokenIDs {
		if !validCallbackTokenID(tokenID) {
			return errors.New("callback presentation token identity is invalid")
		}
		if _, duplicate := seen[tokenID]; duplicate {
			return errors.New("callback presentation token identities must be unique")
		}
		seen[tokenID] = struct{}{}
	}
	return nil
}
func validOutboundBinding(operationID string, updateID int64) bool {
	if operationID == "" && updateID == 0 {
		return true
	}
	return operationID != "" && len(operationID) <= 256 && utf8.ValidString(operationID) && updateID > 0
}
func validArtifactRetryBinding(binding *ArtifactRetryBinding) bool {
	return binding != nil && binding.PresentationID != "" && binding.SessionID != "" && binding.PresentationID != binding.SessionID &&
		binding.MessageID != "" && len(binding.MessageID) <= 1024 && binding.FinalOperationID == binding.MessageID+":final" &&
		binding.Generation > 0 && binding.Slot > 0 && binding.ExpiresAt.Unix() > 0 && binding.ExpiresAt.Nanosecond() == 0
}
func cloneArtifactRetryBinding(binding *ArtifactRetryBinding) *ArtifactRetryBinding {
	return cloneBinding(binding)
}
func callbackSpecialBindingCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}
func validateCallbackClaim(claim CallbackClaim) error {
	if claim.SessionID == "" || claim.Carrier.ChatID <= 0 || claim.Carrier.MessageID <= 0 ||
		!validCallbackTokenID(claim.TokenID) || claim.ExpiresAt.IsZero() || claim.ExpiresAt.Nanosecond() != 0 ||
		claim.UpdateID <= 0 || claim.CallbackQueryID == "" {
		return errors.New("callback claim identity is required")
	}
	return nil
}
