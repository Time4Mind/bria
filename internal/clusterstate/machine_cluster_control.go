package clusterstate

import (
	"encoding/json"

	"github.com/Time4Mind/bria/internal/domain"
)

func applyClusterControl(
	state *domain.State,
	command Command,
) (json.RawMessage, error) {
	switch command.Kind {
	case CommandRequestQuotaRefresh:
		state.RequestQuotaRefresh(command.IssuedAt)
		return nil, nil
	case CommandSetLeaderSelectionMode:
		var payload SetLeaderSelectionMode
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetLeaderSelectionMode(payload.Mode)
		})
	case CommandSetPreferredLeader:
		var payload SetPreferredLeader
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetPreferredLeader(payload.NodeID)
		})
	case CommandSetTemporaryLeader:
		var payload SetTemporaryLeader
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetTemporaryLeader(payload.NodeID, payload.Until, command.IssuedAt)
		})
	case CommandClearTemporaryLeader:
		var payload ClearTemporaryLeader
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			state.ClearTemporaryLeader(payload.NodeID, payload.ObservedUntil)
			return nil
		})
	case CommandIssueEnrollmentInvite:
		var payload domain.EnrollmentInvite
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.IssueEnrollmentInvite(payload, command.IssuedAt)
		})
	case CommandSubmitEnrollment:
		var payload SubmitEnrollment
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SubmitEnrollment(payload.Request, payload.ExpectedHash, command.IssuedAt)
		})
	case CommandSubmitNodeContract:
		var payload domain.EnrollmentRequest
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SubmitNodeContract(payload, command.IssuedAt)
		})
	case CommandDecideEnrollment:
		var payload DecideEnrollment
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.DecideEnrollment(payload.RequestID, payload.Approve, command.IssuedAt)
		})
	case CommandMarkEnrollmentNotified:
		var payload MarkEnrollmentNotified
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.MarkEnrollmentNotified(payload.RequestID, command.IssuedAt)
		})
	case CommandRenameNode:
		var payload RenameNode
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.RenameNode(payload.NodeID, payload.Name)
		})
	case CommandUpdateNodeMetadata:
		var payload domain.Node
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.UpdateNodeMetadata(payload)
		})
	case CommandSetNodeLifecycle:
		var payload SetNodeLifecycle
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetNodeLifecycle(payload.NodeID, payload.Lifecycle)
		})
	case CommandSetNodeIsolation:
		var payload SetNodeIsolation
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetNodeBackendIsolationRequired(payload.NodeID, payload.Required)
		})
	case CommandDeleteNode:
		var payload DeleteNode
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.DeleteDisabledNode(payload.NodeID, command.IssuedAt)
		})
	case CommandBeginClusterUpdate:
		var payload domain.ClusterUpdate
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.BeginClusterUpdate(payload, command.IssuedAt)
		})
	case CommandSetClusterUpdateNode:
		var payload SetClusterUpdateNode
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetClusterUpdateNode(
				payload.UpdateID, payload.NodeID, payload.Phase, payload.Error, command.IssuedAt,
			)
		})
	case CommandFinishClusterUpdate:
		var payload FinishClusterUpdate
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.FinishClusterUpdate(payload.UpdateID, payload.Failed, payload.Error, command.IssuedAt)
		})
	default:
		return nil, domain.ErrInvalidState
	}
}
