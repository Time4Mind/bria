package clusterstate

import (
	"encoding/json"
	"fmt"

	"github.com/Time4Mind/bria/internal/domain"
)

func applySessionLifecycle(state *domain.State, command Command) (json.RawMessage, error) {
	switch command.Kind {
	case CommandClearSession:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, &payload, func() error {
			return state.ClearSession(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				command.OperationID, command.IssuedAt,
			)
		})
	case CommandRenameSession:
		var payload RenameSession
		return nil, decodeAnd(command.Payload, &payload, func() error {
			return state.RenameSessionWithFormat(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				payload.Name, payload.NameFormatVersion, command.IssuedAt,
			)
		})
	case CommandCloseSession:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, &payload, func() error {
			return state.CloseSession(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				payload.ArchiveCommitID, command.IssuedAt,
			)
		})
	case CommandCompleteSessionArchive:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, &payload, func() error {
			return state.CompleteSessionArchive(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				payload.ArchiveCommitID, command.IssuedAt,
			)
		})
	case CommandRestoreSession:
		var payload SessionRevision
		return nil, decodeAnd(command.Payload, &payload, func() error {
			return state.RestoreSession(
				payload.ActorID, payload.Session, payload.ExpectedRevision, command.IssuedAt,
			)
		})
	case CommandArchiveSession:
		var payload ArchiveSession
		return nil, decodeAnd(command.Payload, &payload, func() error {
			return state.ArchiveSession(
				payload.Session, payload.ExpectedRevision, payload.Reason, command.IssuedAt,
			)
		})
	default:
		return nil, fmt.Errorf("unsupported session lifecycle command: %q", command.Kind)
	}
}
