package clusterstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Time4Mind/bria/internal/domain"
)

const defaultLedgerLimit = 4096

type Result struct {
	OperationID string          `json:"operation_id"`
	Value       json.RawMessage `json:"value,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func (r Result) Err() error {
	if r.Error == "" {
		return nil
	}
	return errors.New(r.Error)
}

type Machine struct {
	mu          sync.RWMutex
	state       *domain.State
	ledger      map[string]Result
	ledgerOrder []string
	ledgerLimit int
}

func NewMachine(initial *domain.State) *Machine {
	if initial == nil {
		initial = domain.NewState()
	}
	return &Machine{
		state:       initial.Clone(),
		ledger:      make(map[string]Result),
		ledgerLimit: defaultLedgerLimit,
	}
}

func (m *Machine) State() *domain.State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.Clone()
}

func (m *Machine) Apply(command Command) Result {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.ledger[command.OperationID]; ok {
		return existing
	}
	result := Result{OperationID: command.OperationID}
	if err := validateCommand(command); err != nil {
		result.Error = err.Error()
		m.remember(result)
		return result
	}
	if command.Kind == CommandRestoreCluster {
		return m.applyClusterRestore(command, result)
	}

	candidate := m.state.Clone()
	value, err := apply(candidate, command)
	if err != nil {
		result.Error = err.Error()
	} else {
		m.state = candidate
		result.Value = value
	}
	m.remember(result)
	return result
}

func validateCommand(command Command) error {
	if command.Version < 1 || command.Version > CommandVersion {
		return fmt.Errorf("unsupported command version: %d", command.Version)
	}
	if strings.TrimSpace(command.OperationID) == "" || len(command.OperationID) > 128 {
		return errors.New("operation id must contain 1 to 128 characters")
	}
	if command.IssuedAt.IsZero() {
		return errors.New("issued_at is required")
	}
	return nil
}

func apply(state *domain.State, command Command) (json.RawMessage, error) {
	switch command.Kind {
	case CommandAddNode:
		var payload domain.Node
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			if payload.CreatedAt.IsZero() {
				payload.CreatedAt = command.IssuedAt
			}
			return state.AddNode(payload)
		})
	case CommandSetNodeAccess:
		var payload SetNodeAccess
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetNodeAccess(payload.UserID, payload.Role, payload.NodeIDs...)
		})
	case CommandSetSoleOwner:
		var payload SetSoleOwner
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetSoleOwner(payload.UserID)
		})
	case CommandAddSession:
		var payload domain.Session
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.AddSession(payload)
		})
	case CommandBindProviderSession:
		var payload BindProviderSession
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.BindProviderSession(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				payload.ProviderID, command.IssuedAt,
			)
		})
	case CommandRecoverArchivedSession:
		var payload RecoverArchivedSession
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.RecoverArchivedSession(
				payload.ActorID, payload.Session, payload.ExpectedRevision,
				payload.ProviderID, command.IssuedAt,
			)
		})
	case CommandShareSession:
		var payload ShareSession
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.ShareSession(
				payload.ActorID,
				payload.Session,
				payload.RecipientID,
				payload.Mode,
			)
		})
	case CommandRevokeShare:
		var payload RevokeShare
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.RevokeSessionShare(payload.ActorID, payload.Session, payload.UserID)
		})
	case CommandObserveBoot:
		var payload ObserveBoot
		if err := json.Unmarshal(command.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode observe_boot: %w", err)
		}
		plan, err := state.ObserveNodeBoot(payload.NodeID, payload.BootID, command.IssuedAt)
		if err != nil {
			return nil, err
		}
		return json.Marshal(plan)
	case CommandMarkMissing:
		var payload MarkMissing
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			if payload.ArchiveID == "" {
				payload.ArchiveID = MissingArchiveID(command.OperationID)
			}
			return state.MarkMissingOnSameBoot(
				payload.Session, payload.ArchiveID, payload.ExpectedGeneration,
				payload.ExpectedRevision, payload.CheckVersion, command.IssuedAt,
			)
		})
	case CommandSetPreferences:
		var payload SetPreferences
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetPreferences(payload.UserID, payload.Preferences)
		})
	case CommandSetProviderAlias:
		var payload SetProviderAlias
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetProviderAccountAlias(payload.NodeID, payload.Backend, payload.Alias)
		})
	case CommandSetNodeBackend:
		var payload SetNodeBackend
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SetNodeBackendConnected(payload.NodeID, payload.Backend, payload.Connected)
		})
	case CommandSelectNode:
		var payload SelectNode
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SelectNode(payload.UserID, payload.NodeID, command.IssuedAt)
		})
	case CommandSelectSession:
		var payload SelectSession
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.SelectSession(payload.UserID, payload.Session, command.IssuedAt)
		})
	case CommandBindTelegramBot:
		var payload BindTelegramBot
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.BindTelegramBot(payload.BotID)
		})
	case CommandAdvanceTelegramCursor:
		var payload AdvanceTelegramCursor
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.AdvanceTelegramCursor(payload.NextUpdateID)
		})
	case CommandRecordTelegramCard:
		var payload RecordTelegramCard
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.RecordTelegramResponseCard(payload.UserID, payload.Card)
		})
	case CommandMarkBackgroundNotified:
		var payload MarkBackgroundNotified
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.MarkBackgroundNotified(
				payload.UserID, payload.Session, payload.EventRevision,
			)
		})
	case CommandRequestQuotaRefresh, CommandSetLeaderSelectionMode, CommandSetPreferredLeader,
		CommandSetTemporaryLeader, CommandClearTemporaryLeader,
		CommandIssueEnrollmentInvite, CommandSubmitEnrollment, CommandDecideEnrollment,
		CommandSubmitNodeContract, CommandMarkEnrollmentNotified,
		CommandRenameNode, CommandUpdateNodeMetadata,
		CommandSetNodeLifecycle, CommandDeleteNode, CommandSetNodeIsolation,
		CommandBeginClusterUpdate, CommandSetClusterUpdateNode, CommandFinishClusterUpdate:
		return applyClusterControl(state, command)
	case CommandUpdateNodeRuntime:
		var payload UpdateNodeRuntime
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.UpdateNodeRuntime(
				payload.NodeID, payload.Status, payload.Version, payload.Backends, command.IssuedAt,
			)
		})
	case CommandPublishNodeHeartbeat:
		var payload PublishNodeHeartbeat
		if err := json.Unmarshal(command.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode publish_node_heartbeat: %w", err)
		}
		plan, err := state.PublishNodeHeartbeat(
			payload.NodeID, payload.BootID, payload.Version, payload.OS, payload.Arch,
			payload.CertificateFingerprint, payload.PreviousCertificateFingerprint,
			payload.Backends,
			payload.Archives, payload.Interactive, payload.Finals, command.IssuedAt,
			payload.BackendIsolation,
		)
		if err != nil {
			return nil, err
		}
		if err := state.PublishNodeQuotas(payload.NodeID, payload.Quotas); err != nil {
			return nil, err
		}
		return json.Marshal(plan)
	case CommandMarkNodeOffline:
		var payload MarkNodeOffline
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.MarkNodeOffline(payload.NodeID, payload.ObservedLastSeenAt)
		})
	case CommandCompleteBootRecovery:
		var payload BootRecovery
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.CompleteBootRecovery(payload.Session, command.IssuedAt)
		})
	case CommandFailBootRecovery:
		var payload BootRecovery
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.FailBootRecovery(payload.Session, command.IssuedAt)
		})
	case CommandPublishSessionRuntime:
		var payload PublishSessionRuntime
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.PublishSessionRuntimeWithIssue(
				payload.Session,
				payload.Generation,
				payload.Phase,
				payload.Result,
				payload.Issue,
				command.IssuedAt,
			)
		})
	case CommandRecordSessionActivity:
		var payload RecordSessionActivity
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.RecordSessionActivity(payload.ActorID, payload.Session, command.IssuedAt)
		})
	case CommandQueueDeferredInput:
		var payload QueueDeferredInput
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.QueueDeferredSessionInput(payload.Input, command.IssuedAt)
		})
	case CommandResolveDeferredInput:
		var payload ResolveDeferredInput
		return nil, decodeAnd(command.Payload, command.StrictPayload, &payload, func() error {
			return state.ResolveDeferredSessionInput(
				payload.Session, payload.OperationID, payload.Failed, payload.Detail, command.IssuedAt,
			)
		})
	case CommandClearSession, CommandRenameSession, CommandSetArchiveDescription,
		CommandCloseSession,
		CommandDiscardSession, CommandCompleteSessionDiscard,
		CommandReattachSessionRuntime,
		CommandCompleteSessionArchive, CommandRestoreSession, CommandArchiveSession,
		CommandPurgeSession:
		return applySessionLifecycle(state, command)
	default:
		return nil, fmt.Errorf("unsupported command kind: %q", command.Kind)
	}
}
