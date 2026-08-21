package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type RecoveryOutcome string

const (
	RecoveryComplete  RecoveryOutcome = "complete"
	RecoveryFailed    RecoveryOutcome = "failed"
	RecoveryMissing   RecoveryOutcome = "missing"
	RecoveryDiscarded RecoveryOutcome = "discarded"
)

type RecoveryReport struct {
	ReportID           string            `json:"report_id"`
	NodeID             string            `json:"node_id"`
	Session            domain.SessionRef `json:"session"`
	Outcome            RecoveryOutcome   `json:"outcome"`
	ArchiveID          string            `json:"archive_id,omitempty"`
	ExpectedGeneration uint64            `json:"expected_generation,omitempty"`
	ExpectedRevision   uint64            `json:"expected_revision,omitempty"`
	CheckVersion       bool              `json:"check_version,omitempty"`
	ActorID            domain.UserID     `json:"actor_id,omitempty"`
}

type RecoveryCommitter interface {
	CommitRecovery(context.Context, RecoveryReport) error
}

func (c *ConsensusHeartbeatCommitter) CommitRecovery(
	ctx context.Context,
	report RecoveryReport,
) error {
	if report.ReportID == "" || report.Session.NodeID != domain.NodeID(report.NodeID) {
		return errors.New("recovery report identity is invalid")
	}
	if err := report.Session.Validate(); err != nil {
		return err
	}
	kind := clusterstate.CommandCompleteBootRecovery
	payload := any(clusterstate.BootRecovery{Session: report.Session})
	if report.Outcome == RecoveryFailed {
		kind = clusterstate.CommandFailBootRecovery
	} else if report.Outcome == RecoveryMissing {
		if report.ArchiveID == "" {
			report.ArchiveID = clusterstate.MissingArchiveID(report.ReportID)
		}
		kind = clusterstate.CommandMarkMissing
		payload = clusterstate.MarkMissing{
			Session: report.Session, ArchiveID: report.ArchiveID,
			ExpectedGeneration: report.ExpectedGeneration,
			ExpectedRevision:   report.ExpectedRevision, CheckVersion: report.CheckVersion,
		}
	} else if report.Outcome == RecoveryDiscarded {
		if report.ActorID <= 0 || report.ExpectedRevision == 0 {
			return errors.New("discard recovery identity is invalid")
		}
		kind = clusterstate.CommandCompleteSessionDiscard
		payload = clusterstate.SessionRevision{
			ActorID: report.ActorID, Session: report.Session,
			ExpectedRevision: report.ExpectedRevision,
		}
	} else if report.Outcome != RecoveryComplete {
		return errors.New("recovery outcome is invalid")
	}
	command, err := clusterstate.NewCommand(
		recoveryOperationID(report.NodeID, report.ReportID), kind, c.now(),
		payload,
	)
	if err != nil {
		return err
	}
	result, err := c.apply.Apply(ctx, command)
	if err != nil {
		return fmt.Errorf("commit recovery result: %w", err)
	}
	return result.Err()
}

type RecoveryReporter interface {
	ReportRecovery(context.Context, string, RecoveryReport) error
}

// RemoteRecoveryApplier lets the existing host-local recovery executor report
// its two bounded transitions to whatever node Raft currently elected.
type RemoteRecoveryApplier struct {
	nodeID   string
	leaders  Leadership
	reporter RecoveryReporter
}

func NewRemoteRecoveryApplier(
	nodeID string,
	leaders Leadership,
	reporter RecoveryReporter,
) (*RemoteRecoveryApplier, error) {
	if nodeID == "" || leaders == nil || reporter == nil {
		return nil, errors.New("remote recovery dependencies are required")
	}
	return &RemoteRecoveryApplier{nodeID: nodeID, leaders: leaders, reporter: reporter}, nil
}

func (a *RemoteRecoveryApplier) Apply(
	ctx context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	if command.Kind != clusterstate.CommandCompleteBootRecovery &&
		command.Kind != clusterstate.CommandFailBootRecovery &&
		command.Kind != clusterstate.CommandMarkMissing &&
		command.Kind != clusterstate.CommandCompleteSessionDiscard {
		return clusterstate.Result{}, errors.New("only local runtime recovery results may use the recovery API")
	}
	var payload clusterstate.MarkMissing
	actorID := domain.UserID(0)
	if command.Kind == clusterstate.CommandCompleteSessionDiscard {
		var discard clusterstate.SessionRevision
		if err := json.Unmarshal(command.Payload, &discard); err != nil {
			return clusterstate.Result{}, fmt.Errorf("decode discard transition: %w", err)
		}
		payload.Session = discard.Session
		payload.ExpectedRevision = discard.ExpectedRevision
		actorID = discard.ActorID
	} else if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return clusterstate.Result{}, fmt.Errorf("decode recovery transition: %w", err)
	}
	if payload.Session.NodeID != domain.NodeID(a.nodeID) {
		return clusterstate.Result{}, domain.ErrAccessDenied
	}
	leaderID := a.leaders.LeaderID()
	if leaderID == "" {
		return clusterstate.Result{}, errors.New("Raft leader is not known")
	}
	outcome := RecoveryComplete
	if command.Kind == clusterstate.CommandFailBootRecovery {
		outcome = RecoveryFailed
	} else if command.Kind == clusterstate.CommandMarkMissing {
		outcome = RecoveryMissing
	} else if command.Kind == clusterstate.CommandCompleteSessionDiscard {
		outcome = RecoveryDiscarded
	}
	err := a.reporter.ReportRecovery(ctx, leaderID, RecoveryReport{
		ReportID: command.OperationID, NodeID: a.nodeID,
		Session: payload.Session, Outcome: outcome, ArchiveID: payload.ArchiveID,
		ExpectedGeneration: payload.ExpectedGeneration,
		ExpectedRevision:   payload.ExpectedRevision, CheckVersion: payload.CheckVersion,
		ActorID: actorID,
	})
	if err != nil {
		return clusterstate.Result{}, err
	}
	return clusterstate.Result{OperationID: command.OperationID}, nil
}

func recoveryOperationID(nodeID, reportID string) string {
	return heartbeatOperationID(nodeID, "recovery\x00"+reportID)
}

var _ interface {
	Apply(context.Context, clusterstate.Command) (clusterstate.Result, error)
} = (*RemoteRecoveryApplier)(nil)
