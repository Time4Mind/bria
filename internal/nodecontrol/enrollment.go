package nodecontrol

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type EnrollmentReport struct {
	ReportID     string                   `json:"report_id"`
	Request      domain.EnrollmentRequest `json:"request"`
	ExpectedHash string                   `json:"expected_hash"`
}

type EnrollmentCommitter interface {
	CommitEnrollment(context.Context, EnrollmentReport) error
}

type ConsensusEnrollmentCommitter struct {
	apply CommandApplier
	now   func() time.Time
}

func NewConsensusEnrollmentCommitter(apply CommandApplier) (*ConsensusEnrollmentCommitter, error) {
	if apply == nil {
		return nil, errors.New("command applier is required")
	}
	return &ConsensusEnrollmentCommitter{apply: apply, now: time.Now}, nil
}

func (c *ConsensusEnrollmentCommitter) CommitEnrollment(
	ctx context.Context,
	report EnrollmentReport,
) error {
	if report.ReportID == "" || report.ExpectedHash == "" {
		return errors.New("enrollment report is incomplete")
	}
	command, err := clusterstate.NewCommand(
		"node-enrollment-"+report.ReportID, clusterstate.CommandSubmitEnrollment, c.now(),
		clusterstate.SubmitEnrollment{Request: report.Request, ExpectedHash: report.ExpectedHash},
	)
	if err != nil {
		return err
	}
	result, err := c.apply.Apply(ctx, command)
	if err != nil {
		return fmt.Errorf("commit enrollment request: %w", err)
	}
	return result.Err()
}
