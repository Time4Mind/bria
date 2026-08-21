package clusterstate

import (
	"encoding/json"
	"fmt"

	"github.com/Time4Mind/bria/internal/domain"
)

type PurgeSession struct {
	Session          domain.SessionRef `json:"session"`
	ArchiveID        string            `json:"archive_id"`
	ExpectedRevision uint64            `json:"expected_revision"`
}

func applySessionLifecycle(state *domain.State, command Command) (json.RawMessage, error) {
	switch command.Kind {
	case CommandClearSession:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.ClearSession(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				command.OperationID, command.IssuedAt,
			)
		})
	case CommandRenameSession:
		var payload RenameSession
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.RenameSessionWithFormat(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				payload.Name, payload.NameFormatVersion, command.IssuedAt,
			)
		})
	case CommandCloseSession:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.CloseSession(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				payload.ArchiveCommitID, command.IssuedAt,
			)
		})
	case CommandDiscardSession:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.DiscardSession(
				payload.ActorID, payload.Session, payload.ExpectedRevision, command.IssuedAt,
			)
		})
	case CommandCompleteSessionDiscard:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.CompleteSessionDiscard(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
			)
		})
	case CommandReattachSessionRuntime:
		var payload ReattachSessionRuntime
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.ReattachSessionRuntime(
				payload.Session, payload.ExpectedGeneration, payload.ExpectedRevision,
				command.IssuedAt,
			)
		})
	case CommandCompleteSessionArchive:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.CompleteSessionArchive(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				payload.ArchiveCommitID, command.IssuedAt,
			)
		})
	case CommandRestoreSession:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.RestoreSession(
				payload.ActorID, payload.Session, payload.ExpectedRevision, command.IssuedAt,
			)
		})
	case CommandArchiveSession:
		var payload ArchiveSession
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.ArchiveSession(
				payload.Session, payload.ExpectedRevision, payload.Reason, command.IssuedAt,
			)
		})
	case CommandPurgeSession:
		var payload PurgeSession
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.PurgeSession(
				payload.Session, payload.ArchiveID, payload.ExpectedRevision, command.IssuedAt,
			)
		})
	default:
		return nil, fmt.Errorf("unsupported session lifecycle command: %q", command.Kind)
	}
}
