// Package clusterstate applies versioned deterministic commands to domain state.
package clusterstate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

const CommandVersion = 2

type CommandKind string

const (
	CommandAddNode                CommandKind = "add_node"
	CommandSetNodeAccess          CommandKind = "set_node_access"
	CommandSetSoleOwner           CommandKind = "set_sole_owner"
	CommandAddSession             CommandKind = "add_session"
	CommandBindProviderSession    CommandKind = "bind_provider_session"
	CommandShareSession           CommandKind = "share_session"
	CommandRevokeShare            CommandKind = "revoke_share"
	CommandObserveBoot            CommandKind = "observe_boot"
	CommandMarkMissing            CommandKind = "mark_missing"
	CommandSetPreferences         CommandKind = "set_preferences"
	CommandSelectNode             CommandKind = "select_node"
	CommandSelectSession          CommandKind = "select_session"
	CommandBindTelegramBot        CommandKind = "bind_telegram_bot"
	CommandAdvanceTelegramCursor  CommandKind = "advance_telegram_cursor"
	CommandRecordTelegramCard     CommandKind = "record_telegram_card"
	CommandMarkBackgroundNotified CommandKind = "mark_background_notified"
	CommandRequestQuotaRefresh    CommandKind = "request_quota_refresh"
	CommandSetTemporaryLeader     CommandKind = "set_temporary_leader"
	CommandClearTemporaryLeader   CommandKind = "clear_temporary_leader"
	CommandUpdateNodeRuntime      CommandKind = "update_node_runtime"
	CommandPublishNodeHeartbeat   CommandKind = "publish_node_heartbeat"
	CommandMarkNodeOffline        CommandKind = "mark_node_offline"
	CommandCompleteBootRecovery   CommandKind = "complete_boot_recovery"
	CommandFailBootRecovery       CommandKind = "fail_boot_recovery"
	CommandPublishSessionRuntime  CommandKind = "publish_session_runtime"
	CommandRecordSessionActivity  CommandKind = "record_session_activity"
	CommandClearSession           CommandKind = "clear_session"
	CommandRenameSession          CommandKind = "rename_session"
	CommandSetArchiveDescription  CommandKind = "set_archive_description"
	CommandCloseSession           CommandKind = "close_session"
	CommandDiscardSession         CommandKind = "discard_session"
	CommandCompleteSessionDiscard CommandKind = "complete_session_discard"
	CommandReattachSessionRuntime CommandKind = "reattach_session_runtime"
	CommandCompleteSessionArchive CommandKind = "complete_session_archive"
	CommandRestoreSession         CommandKind = "restore_session"
	CommandArchiveSession         CommandKind = "archive_session"
	CommandPurgeSession           CommandKind = "purge_session"
	CommandIssueEnrollmentInvite  CommandKind = "issue_enrollment_invite"
	CommandSubmitEnrollment       CommandKind = "submit_enrollment"
	CommandSubmitNodeContract     CommandKind = "submit_node_contract"
	CommandDecideEnrollment       CommandKind = "decide_enrollment"
	CommandMarkEnrollmentNotified CommandKind = "mark_enroll_notified"
	CommandRenameNode             CommandKind = "rename_node"
	CommandUpdateNodeMetadata     CommandKind = "update_node_metadata"
	CommandSetNodeLifecycle       CommandKind = "set_node_lifecycle"
	CommandDeleteNode             CommandKind = "delete_node"
	CommandRestoreCluster         CommandKind = "restore_cluster"
	CommandSetProviderAlias       CommandKind = "set_provider_alias"
	CommandSetNodeBackend         CommandKind = "set_node_backend"
	CommandSetNodeIsolation       CommandKind = "set_node_isolation"
	CommandQueueDeferredInput     CommandKind = "queue_deferred_input"
	CommandResolveDeferredInput   CommandKind = "resolve_deferred_input"
	CommandBeginClusterUpdate     CommandKind = "begin_cluster_update"
	CommandSetClusterUpdateNode   CommandKind = "set_cluster_update_node"
	CommandFinishClusterUpdate    CommandKind = "finish_cluster_update"
)

type Command struct {
	Version       int             `json:"version"`
	OperationID   string          `json:"operation_id"`
	Kind          CommandKind     `json:"kind"`
	IssuedAt      time.Time       `json:"issued_at"`
	StrictPayload bool            `json:"strict_payload,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

func NewCommand(operationID string, kind CommandKind, at time.Time, payload any) (Command, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Command{}, fmt.Errorf("encode command payload: %w", err)
	}
	return Command{
		Version:       CommandVersion,
		OperationID:   operationID,
		Kind:          kind,
		IssuedAt:      at.UTC(),
		StrictPayload: true,
		Payload:       encoded,
	}, nil
}

type SetNodeAccess struct {
	UserID  domain.UserID   `json:"user_id"`
	Role    domain.Role     `json:"role"`
	NodeIDs []domain.NodeID `json:"node_ids"`
}

type SetSoleOwner struct {
	UserID domain.UserID `json:"user_id"`
}

type ShareSession struct {
	ActorID     domain.UserID     `json:"actor_id"`
	Session     domain.SessionRef `json:"session"`
	RecipientID domain.UserID     `json:"recipient_id"`
	Mode        domain.ShareMode  `json:"mode"`
}

type RevokeShare struct {
	ActorID domain.UserID     `json:"actor_id"`
	Session domain.SessionRef `json:"session"`
	UserID  domain.UserID     `json:"user_id"`
}

type ObserveBoot struct {
	NodeID domain.NodeID `json:"node_id"`
	BootID string        `json:"boot_id"`
}

type MarkMissing struct {
	Session            domain.SessionRef `json:"session"`
	ArchiveID          string            `json:"archive_id"`
	ExpectedGeneration uint64            `json:"expected_generation,omitempty"`
	ExpectedRevision   uint64            `json:"expected_revision,omitempty"`
	CheckVersion       bool              `json:"check_version,omitempty"`
}

func MissingArchiveID(operationID string) string {
	digest := sha256.Sum256([]byte(operationID))
	return fmt.Sprintf("missing-%x", digest[:16])
}

type BootRecovery struct {
	Session domain.SessionRef `json:"session"`
}

type SetPreferences struct {
	UserID      domain.UserID          `json:"user_id"`
	Preferences domain.UserPreferences `json:"preferences"`
}

type SelectNode struct {
	UserID domain.UserID `json:"user_id"`
	NodeID domain.NodeID `json:"node_id"`
}

type SelectSession struct {
	UserID  domain.UserID     `json:"user_id"`
	Session domain.SessionRef `json:"session"`
}

type BindProviderSession struct {
	ActorID          domain.UserID     `json:"actor_id"`
	Session          domain.SessionRef `json:"session"`
	ExpectedRevision uint64            `json:"expected_revision"`
	ProviderID       string            `json:"provider_id"`
}

type AdvanceTelegramCursor struct {
	NextUpdateID int64 `json:"next_update_id"`
}

type BindTelegramBot struct {
	BotID int64 `json:"bot_id"`
}

type RecordTelegramCard struct {
	UserID domain.UserID               `json:"user_id"`
	Card   domain.TelegramResponseCard `json:"card"`
}

type MarkBackgroundNotified struct {
	UserID        domain.UserID     `json:"user_id"`
	Session       domain.SessionRef `json:"session"`
	EventRevision uint64            `json:"event_revision"`
}

type SetTemporaryLeader struct {
	NodeID domain.NodeID `json:"node_id"`
	Until  time.Time     `json:"until"`
}

type ClearTemporaryLeader struct {
	NodeID        domain.NodeID `json:"node_id"`
	ObservedUntil time.Time     `json:"observed_until"`
}

type UpdateNodeRuntime struct {
	NodeID   domain.NodeID              `json:"node_id"`
	Status   domain.NodeStatus          `json:"status"`
	Version  string                     `json:"version,omitempty"`
	Backends []domain.BackendDescriptor `json:"backends,omitempty"`
}

type PublishNodeHeartbeat struct {
	NodeID                         domain.NodeID                    `json:"node_id"`
	BootID                         string                           `json:"boot_id"`
	Version                        string                           `json:"version,omitempty"`
	OS                             string                           `json:"os,omitempty"`
	Arch                           string                           `json:"arch,omitempty"`
	CertificateFingerprint         string                           `json:"certificate_fingerprint,omitempty"`
	PreviousCertificateFingerprint string                           `json:"previous_certificate_fingerprint,omitempty"`
	Backends                       []domain.BackendDescriptor       `json:"backends,omitempty"`
	Archives                       []string                         `json:"archives,omitempty"`
	Interactive                    []domain.InteractivePromptReport `json:"interactive,omitempty"`
	Finals                         []domain.TranscriptFinalReport   `json:"transcript_finals,omitempty"`
	Quotas                         []domain.QuotaSnapshot           `json:"quotas,omitempty"`
	BackendIsolation               domain.BackendIsolationReport    `json:"backend_isolation,omitempty"`
}

type MarkNodeOffline struct {
	NodeID             domain.NodeID `json:"node_id"`
	ObservedLastSeenAt time.Time     `json:"observed_last_seen_at"`
}

type PublishSessionRuntime struct {
	Session    domain.SessionRef              `json:"session"`
	Generation uint64                         `json:"generation"`
	Phase      domain.RuntimePhase            `json:"phase"`
	Result     *domain.SessionOperationResult `json:"result,omitempty"`
}

type RecordSessionActivity struct {
	ActorID domain.UserID     `json:"actor_id"`
	Session domain.SessionRef `json:"session"`
}

type SessionRevision struct {
	ActorID          domain.UserID     `json:"actor_id"`
	Session          domain.SessionRef `json:"session"`
	ExpectedRevision uint64            `json:"expected_revision"`
	ArchiveCommitID  string            `json:"archive_commit_id,omitempty"`
}

type RenameSession struct {
	ActorID           domain.UserID     `json:"actor_id"`
	Session           domain.SessionRef `json:"session"`
	ExpectedRevision  uint64            `json:"expected_revision"`
	Name              string            `json:"name"`
	NameFormatVersion int               `json:"name_format_version,omitempty"`
}

type SetArchiveDescription struct {
	Session          domain.SessionRef `json:"session"`
	ExpectedRevision uint64            `json:"expected_revision"`
	ArchiveID        string            `json:"archive_id"`
	Lines            []string          `json:"lines"`
	Version          int               `json:"version"`
}

type ArchiveSession struct {
	Session          domain.SessionRef    `json:"session"`
	ExpectedRevision uint64               `json:"expected_revision"`
	Reason           domain.ArchiveReason `json:"reason"`
}

type SubmitEnrollment struct {
	Request      domain.EnrollmentRequest `json:"request"`
	ExpectedHash string                   `json:"expected_hash"`
}

type DecideEnrollment struct {
	RequestID string `json:"request_id"`
	Approve   bool   `json:"approve"`
}

type MarkEnrollmentNotified struct {
	RequestID string `json:"request_id"`
}

type RenameNode struct {
	NodeID domain.NodeID `json:"node_id"`
	Name   string        `json:"name"`
}

type SetNodeLifecycle struct {
	NodeID    domain.NodeID        `json:"node_id"`
	Lifecycle domain.NodeLifecycle `json:"lifecycle"`
}

type DeleteNode struct {
	NodeID domain.NodeID `json:"node_id"`
}

type RestoreCluster struct {
	BackupSHA256 string `json:"backup_sha256"`
	Snapshot     []byte `json:"snapshot"`
}

type SetProviderAlias struct {
	NodeID  domain.NodeID `json:"node_id"`
	Backend string        `json:"backend"`
	Alias   string        `json:"alias"`
}

type SetNodeBackend struct {
	NodeID    domain.NodeID `json:"node_id"`
	Backend   string        `json:"backend"`
	Connected bool          `json:"connected"`
}

type SetNodeIsolation struct {
	NodeID   domain.NodeID `json:"node_id"`
	Required bool          `json:"required"`
}

type SetClusterUpdateNode struct {
	UpdateID string                 `json:"update_id"`
	NodeID   domain.NodeID          `json:"node_id"`
	Phase    domain.NodeUpdatePhase `json:"phase"`
	Error    string                 `json:"error,omitempty"`
}

type FinishClusterUpdate struct {
	UpdateID string `json:"update_id"`
	Failed   bool   `json:"failed,omitempty"`
	Error    string `json:"error,omitempty"`
}
