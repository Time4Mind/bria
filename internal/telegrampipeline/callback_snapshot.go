package telegrampipeline

import (
	"bria/internal/domain"
	"bria/internal/telegramstate"
	"context"
	"encoding/json"
	"errors"
	"time"
)

const CallbackRegistrySnapshotVersion = callbackRegistryVersion

type CallbackClaimSnapshot struct {
	UpdateID        int64  `json:"update_id"`
	CallbackQueryID string `json:"callback_query_id"`
}

type CallbackPresentationSnapshot struct {
	SessionID            domain.SessionID                 `json:"session_id"`
	Carrier              telegramstate.Carrier            `json:"carrier"`
	ExpiresAt            time.Time                        `json:"expires_at"`
	Tokens               map[string]bool                  `json:"tokens"`
	Claims               map[string]CallbackClaimSnapshot `json:"claims,omitempty"`
	InteractionRequestID string                           `json:"interaction_request_id,omitempty"`
	OutboundOperationID  string                           `json:"outbound_operation_id,omitempty"`
	OutboundUpdateID     int64                            `json:"outbound_update_id,omitempty"`
	Recovery             *CallbackRecoveryBinding         `json:"recovery,omitempty"`
	AcceptedTurnRecovery *AcceptedTurnRecoveryBinding     `json:"accepted_turn_recovery,omitempty"`
	StatusRecovery       *StatusRecoveryBinding           `json:"status_recovery,omitempty"`
}
type CallbackRegistrySnapshot struct {
	Version       int                                               `json:"version"`
	Presentations map[domain.SessionID]CallbackPresentationSnapshot `json:"presentations"`
}

func (snapshot CallbackRegistrySnapshot) Validate() error {
	state, err := callbackRegistryStateFromSnapshot(snapshot)
	if err != nil {
		return err
	}
	if err := validateFileCallbackRegistryState(state); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > maxCallbackRegistrySize {
		return errors.New("callback registry snapshot exceeds size limit")
	}
	return nil
}
func (registry *FileCallbackRegistry) Snapshot(ctx context.Context) (CallbackRegistrySnapshot, error) {
	if ctx == nil || registry == nil {
		return CallbackRegistrySnapshot{}, errors.New("callback registry and context are required")
	}
	if err := ctx.Err(); err != nil {
		return CallbackRegistrySnapshot{}, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	reopened, err := OpenFileCallbackRegistry(registry.path, registry.now)
	if err != nil {
		return CallbackRegistrySnapshot{}, err
	}
	registry.state = cloneFileCallbackRegistryState(reopened.state)
	return callbackRegistrySnapshotFromState(registry.state), nil
}
func callbackRegistrySnapshotFromState(state fileCallbackRegistryState) CallbackRegistrySnapshot {
	result := CallbackRegistrySnapshot{Version: state.Version, Presentations: make(map[domain.SessionID]CallbackPresentationSnapshot, len(state.Presentations))}
	for id, value := range state.Presentations {
		claims := make(map[string]CallbackClaimSnapshot, len(value.Claims))
		for token, claim := range value.Claims {
			claims[token] = CallbackClaimSnapshot{UpdateID: claim.UpdateID, CallbackQueryID: claim.CallbackQueryID}
		}
		result.Presentations[id] = CallbackPresentationSnapshot{SessionID: value.SessionID, Carrier: value.Carrier, ExpiresAt: value.ExpiresAt, Tokens: cloneClaimedTokens(value.Tokens), Claims: claims, InteractionRequestID: value.InteractionRequestID, OutboundOperationID: value.OutboundOperationID, OutboundUpdateID: value.OutboundUpdateID, Recovery: cloneCallbackRecoveryBinding(value.Recovery), AcceptedTurnRecovery: cloneAcceptedTurnRecoveryBinding(value.AcceptedTurnRecovery), StatusRecovery: cloneStatusRecoveryBinding(value.StatusRecovery)}
	}
	return result
}
func callbackRegistryStateFromSnapshot(snapshot CallbackRegistrySnapshot) (fileCallbackRegistryState, error) {
	if snapshot.Version != callbackRegistryVersion || snapshot.Presentations == nil {
		return fileCallbackRegistryState{}, errors.New("callback registry snapshot schema is invalid")
	}
	state := fileCallbackRegistryState{Version: snapshot.Version, Presentations: make(map[domain.SessionID]fileCallbackPresentation, len(snapshot.Presentations))}
	for id, value := range snapshot.Presentations {
		claims := make(map[string]fileCallbackClaimIdentity, len(value.Claims))
		for token, claim := range value.Claims {
			claims[token] = fileCallbackClaimIdentity{UpdateID: claim.UpdateID, CallbackQueryID: claim.CallbackQueryID}
		}
		state.Presentations[id] = fileCallbackPresentation{SessionID: value.SessionID, Carrier: value.Carrier, ExpiresAt: value.ExpiresAt, Tokens: cloneClaimedTokens(value.Tokens), Claims: claims, InteractionRequestID: value.InteractionRequestID, OutboundOperationID: value.OutboundOperationID, OutboundUpdateID: value.OutboundUpdateID, Recovery: cloneCallbackRecoveryBinding(value.Recovery), AcceptedTurnRecovery: cloneAcceptedTurnRecoveryBinding(value.AcceptedTurnRecovery), StatusRecovery: cloneStatusRecoveryBinding(value.StatusRecovery)}
	}
	return state, nil
}
func cloneClaimedTokens(values map[string]bool) map[string]bool {
	result := make(map[string]bool, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
