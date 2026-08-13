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
	Quotas                         []domain.QuotaSnapshot           `json:"quotas,omitempty"`
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
	now   func() time.Time
}

func NewConsensusHeartbeatCommitter(apply CommandApplier) (*ConsensusHeartbeatCommitter, error) {
	if apply == nil {
		return nil, errors.New("command applier is required")
	}
	return &ConsensusHeartbeatCommitter{apply: apply, now: time.Now}, nil
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
			CertificateFingerprint: report.CertificateFingerprint,
			PreviousCertificateFingerprint: report.PreviousCertificateFingerprint,
			Backends:                       report.Backends,
			Archives:                       report.Archives, Interactive: report.Interactive,
			Finals: report.Finals, Quotas: report.Quotas,
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
	return HeartbeatAck{Recovery: plan}, nil
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
	if len(report.Quotas) > 16 {
		return errors.New("heartbeat quota inventory is too large")
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
