package nodecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

const maxHeartbeatReportID = 96

// Heartbeat is a node's actual host-local inventory. It is authenticated by
// mTLS at the server; NodeID is still carried to reject identity confusion.
type Heartbeat struct {
	ReportID                       string                           `json:"report_id"`
	NodeID                         string                           `json:"node_id"`
	BootID                         string                           `json:"boot_id"`
	Version                        string                           `json:"version,omitempty"`
	OS                             string                           `json:"os,omitempty"`
	Arch                           string                           `json:"arch,omitempty"`
	CertificateFingerprint         string                           `json:"-"`
	PreviousCertificateFingerprint string                           `json:"-"`
	Backends                       []domain.BackendDescriptor       `json:"backends,omitempty"`
	Archives                       []string                         `json:"archives,omitempty"`
	Interactive                    []domain.InteractivePromptReport `json:"interactive,omitempty"`
	Finals                         []domain.TranscriptFinalReport   `json:"transcript_finals,omitempty"`
	Runtime                        []domain.TranscriptRuntimeReport `json:"transcript_runtime,omitempty"`
	Quotas                         []domain.QuotaSnapshot           `json:"quotas,omitempty"`
	BackendIsolation               domain.BackendIsolationReport    `json:"backend_isolation,omitempty"`
}

type HeartbeatAck struct {
	Recovery domain.BootRecoveryPlan `json:"recovery"`
}

type HeartbeatCommitter interface {
	CommitHeartbeat(context.Context, Heartbeat) (HeartbeatAck, error)
}

type CommandApplier interface {
	Apply(context.Context, clusterstate.Command) (clusterstate.Result, error)
}

// ConsensusHeartbeatCommitter is used only by the leader-side endpoint. It
// converts authenticated reports to ordinary deterministic Raft commands.
type ConsensusHeartbeatCommitter struct {
	apply CommandApplier
	state StateReader
	now   func() time.Time
}

func NewConsensusHeartbeatCommitter(
	apply CommandApplier,
	readers ...StateReader,
) (*ConsensusHeartbeatCommitter, error) {
	if apply == nil {
		return nil, errors.New("command applier is required")
	}
	if len(readers) > 1 || (len(readers) == 1 && readers[0] == nil) {
		return nil, errors.New("at most one state reader is supported")
	}
	committer := &ConsensusHeartbeatCommitter{apply: apply, now: time.Now}
	if len(readers) == 1 {
		committer.state = readers[0]
	}
	return committer, nil
}

func (c *ConsensusHeartbeatCommitter) CommitHeartbeat(
	ctx context.Context,
	report Heartbeat,
) (HeartbeatAck, error) {
	if err := validateHeartbeat(report); err != nil {
		return HeartbeatAck{}, err
	}
	command, err := clusterstate.NewCommand(
		heartbeatOperationID(report.NodeID, report.ReportID),
		clusterstate.CommandPublishNodeHeartbeat,
		c.now(),
		clusterstate.PublishNodeHeartbeat{
			NodeID: domain.NodeID(report.NodeID), BootID: report.BootID,
			Version: report.Version, OS: report.OS, Arch: report.Arch,
			CertificateFingerprint:         report.CertificateFingerprint,
			PreviousCertificateFingerprint: report.PreviousCertificateFingerprint,
			Backends:                       report.Backends,
			Archives:                       report.Archives, Interactive: report.Interactive,
			Finals: report.Finals, Quotas: report.Quotas,
			BackendIsolation: report.BackendIsolation,
		},
	)
	if err != nil {
		return HeartbeatAck{}, err
	}
	result, err := c.apply.Apply(ctx, command)
	if err != nil {
		return HeartbeatAck{}, fmt.Errorf("commit heartbeat: %w", err)
	}
	if err := result.Err(); err != nil {
		return HeartbeatAck{}, err
	}
	var plan domain.BootRecoveryPlan
	if err := json.Unmarshal(result.Value, &plan); err != nil {
		return HeartbeatAck{}, fmt.Errorf("decode heartbeat recovery plan: %w", err)
	}
	if err := c.commitTranscriptRuntime(ctx, report); err != nil {
		return HeartbeatAck{}, err
	}
	return HeartbeatAck{Recovery: plan}, nil
}

func (c *ConsensusHeartbeatCommitter) commitTranscriptRuntime(
	ctx context.Context,
	report Heartbeat,
) error {
	if len(report.Runtime) == 0 {
		return nil
	}
	if c.state == nil {
		return errors.New("heartbeat runtime reports require a state reader")
	}
	for _, runtimeReport := range report.Runtime {
		if runtimeReport.Timestamp.After(c.now().UTC().Add(5 * time.Minute)) {
			return errors.New("heartbeat transcript runtime is from the future")
		}
		state := c.state.State()
		if state == nil {
			return errors.New("cluster state is unavailable")
		}
		ref := domain.SessionRef{
			NodeID: domain.NodeID(report.NodeID), SessionID: runtimeReport.SessionID,
		}
		session, exists := state.Sessions[ref.Key()]
		if !exists || !session.IsLive() || session.RuntimeGeneration != runtimeReport.Generation ||
			runtimeReport.Timestamp.Before(session.LastEventAt) ||
			session.RuntimePhase == runtimeReport.Phase ||
			(session.RuntimePhase != domain.RuntimeIdle &&
				session.RuntimePhase != domain.RuntimeRunning) {
			continue
		}
		command, err := clusterstate.NewCommand(
			transcriptRuntimeOperationID(report.NodeID, runtimeReport),
			clusterstate.CommandPublishSessionRuntime, runtimeReport.Timestamp,
			clusterstate.PublishSessionRuntime{
				Session: ref, Generation: runtimeReport.Generation, Phase: runtimeReport.Phase,
			},
		)
		if err != nil {
			return err
		}
		result, err := c.apply.Apply(ctx, command)
		if err != nil {
			return fmt.Errorf("commit transcript runtime: %w", err)
		}
		if err := result.Err(); err != nil {
			return fmt.Errorf("commit transcript runtime: %w", err)
		}
	}
	return nil
}

func validateHeartbeat(report Heartbeat) error {
	if strings.TrimSpace(report.ReportID) == "" || len(report.ReportID) > maxHeartbeatReportID {
		return errors.New("heartbeat report id must contain 1 to 96 characters")
	}
	if strings.TrimSpace(report.ReportID) != report.ReportID {
		return errors.New("heartbeat report id must not contain surrounding whitespace")
	}
	if err := (domain.SessionRef{
		NodeID: domain.NodeID(report.NodeID), SessionID: "heartbeat-validation",
	}).Validate(); err != nil {
		return fmt.Errorf("heartbeat node id: %w", err)
	}
	if strings.TrimSpace(report.BootID) == "" || len(report.BootID) > 256 {
		return errors.New("heartbeat boot id must contain 1 to 256 characters")
	}
	platformMissing := strings.TrimSpace(report.OS) == "" && strings.TrimSpace(report.Arch) == ""
	platformComplete := strings.TrimSpace(report.OS) != "" && strings.TrimSpace(report.Arch) != ""
	if (!platformMissing && !platformComplete) || len(report.OS) > 32 || len(report.Arch) > 32 {
		return errors.New("heartbeat platform is invalid")
	}
	if report.CertificateFingerprint != "" && !sha256Hex(report.CertificateFingerprint) {
		return errors.New("heartbeat certificate fingerprint is invalid")
	}
	if report.PreviousCertificateFingerprint != "" &&
		!sha256Hex(report.PreviousCertificateFingerprint) {
		return errors.New("heartbeat previous certificate fingerprint is invalid")
	}
	if len(report.Archives) > 4096 {
		return errors.New("heartbeat archive inventory is too large")
	}
	if len(report.Interactive) > 512 {
		return errors.New("heartbeat interactive inventory is too large")
	}
	if len(report.Finals) > 512 {
		return errors.New("heartbeat transcript final inventory is too large")
	}
	for _, final := range report.Finals {
		if err := (domain.SessionRef{
			NodeID: domain.NodeID(report.NodeID), SessionID: final.SessionID,
		}).Validate(); err != nil || final.Generation == 0 || final.Timestamp.IsZero() ||
			len(final.Digest) != 64 || !lowerHex(final.Digest) {
			return errors.New("heartbeat transcript final is invalid")
		}
	}
	if len(report.Runtime) > 512 {
		return errors.New("heartbeat transcript runtime inventory is too large")
	}
	for _, runtimeReport := range report.Runtime {
		if err := (domain.SessionRef{
			NodeID: domain.NodeID(report.NodeID), SessionID: runtimeReport.SessionID,
		}).Validate(); err != nil || runtimeReport.Generation == 0 ||
			(runtimeReport.Phase != domain.RuntimeIdle &&
				runtimeReport.Phase != domain.RuntimeRunning) ||
			runtimeReport.Timestamp.IsZero() {
			return errors.New("heartbeat transcript runtime is invalid")
		}
	}
	if len(report.Quotas) > 16 {
		return errors.New("heartbeat quota inventory is too large")
	}
	mode := strings.ToLower(strings.TrimSpace(report.BackendIsolation.Mode))
	if mode == "" {
		mode = "trusted"
	}
	if (mode == "trusted" && report.BackendIsolation.Ready) ||
		((mode == "docker" || mode == "native-user" || mode == "wsl") &&
			!report.BackendIsolation.Ready) ||
		(mode != "trusted" && mode != "docker" && mode != "native-user" && mode != "wsl") {
		return errors.New("heartbeat backend isolation report is invalid")
	}
	for _, snapshot := range report.Quotas {
		if snapshot.NodeID != domain.NodeID(report.NodeID) {
			return errors.New("heartbeat quota node mismatch")
		}
		if err := snapshot.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func sha256Hex(value string) bool { return len(value) == sha256.Size*2 && lowerHex(value) }

func lowerHex(value string) bool {
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func heartbeatOperationID(nodeID, reportID string) string {
	digest := sha256.Sum256([]byte(nodeID + "\x00" + reportID))
	return "node-heartbeat-" + hex.EncodeToString(digest[:16])
}

func transcriptRuntimeOperationID(nodeID string, report domain.TranscriptRuntimeReport) string {
	value := fmt.Sprintf(
		"%s\x00%s\x00%d\x00%s\x00%s", nodeID, report.SessionID, report.Generation,
		report.Phase, report.Timestamp.UTC().Format(time.RFC3339Nano),
	)
	digest := sha256.Sum256([]byte(value))
	return "transcript-runtime-" + hex.EncodeToString(digest[:16])
}
