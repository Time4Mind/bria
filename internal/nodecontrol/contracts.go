// Package nodecontrol carries short-lived session operations from the current
// Raft leader to the node that owns the runtime. Payloads are never replicated.
package nodecontrol

import (
	"context"
	"crypto/x509"
	"errors"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

var (
	ErrNotCurrentLeader = errors.New("request did not come from the current leader")
	ErrUnknownMember    = errors.New("request came from an unknown cluster member")
	ErrTargetMismatch   = errors.New("runtime request targets another node")
)

type Leadership interface {
	LeaderID() string
}

type HealthObserver interface {
	Leadership
	Stats() map[string]string
}

type ClusterSnapshotter interface {
	MarshalSnapshot() ([]byte, error)
}

type MembershipAdmin interface {
	Apply(context.Context, clusterstate.Command) (clusterstate.Result, error)
	LeaderID() string
	IsMember(string) bool
	IsVoterAt(string, string) bool
}

type Membership interface {
	IsMember(string) bool
}

type CertificateMembership interface {
	AuthorizeCertificate(string, *x509.Certificate) bool
}

type Authorizer interface {
	AuthorizeRuntime(context.Context, runtimehost.Request) error
}

type Resolver interface {
	ControlAddress(string) (string, bool)
}

type CertificateResolver interface {
	NodeFingerprint(string) (string, bool)
}

type Submitter interface {
	Submit(context.Context, runtimehost.Request) (runtimehost.Receipt, error)
}

type RuntimeClient interface {
	Submitter
	LookupResult(context.Context, runtimehost.Request) (runtimehost.Result, bool, error)
}

type Service struct {
	nodeID     string
	authorizer Authorizer
	executor   runtimehost.Executor
}

func NewService(nodeID string, authorizer Authorizer, executor runtimehost.Executor) (*Service, error) {
	if nodeID == "" || authorizer == nil || executor == nil {
		return nil, errors.New("node id, authorizer, and executor are required")
	}
	return &Service{nodeID: nodeID, authorizer: authorizer, executor: executor}, nil
}

func (s *Service) Submit(
	ctx context.Context,
	request runtimehost.Request,
) (runtimehost.Receipt, error) {
	if request.NodeID != s.nodeID {
		return runtimehost.Receipt{}, ErrTargetMismatch
	}
	if err := s.authorizer.AuthorizeRuntime(ctx, request); err != nil {
		return runtimehost.Receipt{}, err
	}
	return s.executor.Submit(ctx, request)
}

func (s *Service) LookupResult(
	ctx context.Context,
	request runtimehost.Request,
) (runtimehost.Result, bool, error) {
	if request.NodeID != s.nodeID {
		return runtimehost.Result{}, false, ErrTargetMismatch
	}
	if err := s.authorizer.AuthorizeRuntime(ctx, request); err != nil {
		return runtimehost.Result{}, false, err
	}
	return s.executor.LookupResult(ctx, request.OperationID)
}
