// Package recovery resumes host-local sessions after an OS reboot.
package recovery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type StateReader interface {
	State() *domain.State
}

type CommandApplier interface {
	Apply(context.Context, clusterstate.Command) (clusterstate.Result, error)
}

type Runtime interface {
	// Resume must be idempotent for one operation ID. A retry must return the
	// already-created runtime session instead of starting a duplicate process.
	Resume(context.Context, domain.Session, string) error
}

type Executor struct {
	nodeID  domain.NodeID
	state   StateReader
	apply   CommandApplier
	runtime Runtime
	now     func() time.Time
	newID   func() (string, error)
}

func NewExecutor(
	nodeID domain.NodeID,
	state StateReader,
	apply CommandApplier,
	runtime Runtime,
) (*Executor, error) {
	if err := (domain.SessionRef{NodeID: nodeID, SessionID: "validation"}).Validate(); err != nil {
		return nil, fmt.Errorf("node id: %w", err)
	}
	if state == nil || apply == nil || runtime == nil {
		return nil, errors.New("state, command applier, and runtime are required")
	}
	return &Executor{
		nodeID:  nodeID,
		state:   state,
		apply:   apply,
		runtime: runtime,
		now:     time.Now,
		newID:   operationID,
	}, nil
}

func (e *Executor) Run(ctx context.Context, refs []domain.SessionRef) error {
	var failures []error
	for _, ref := range refs {
		if err := e.recoverOne(ctx, ref); err != nil {
			failures = append(failures, fmt.Errorf("recover %s: %w", ref.Key(), err))
		}
	}
	return errors.Join(failures...)
}

func (e *Executor) recoverOne(ctx context.Context, ref domain.SessionRef) error {
	if ref.NodeID != e.nodeID {
		return domain.ErrAccessDenied
	}
	state := e.state.State()
	session, ok := state.Sessions[ref.Key()]
	if !ok || !session.IsLive() || !session.ResumePending {
		return domain.ErrNotFound
	}
	node, ok := state.Nodes[e.nodeID]
	if !ok || node.BootID == "" {
		return fmt.Errorf("current node boot id is unavailable")
	}
	runtimeOperationID := stableRecoveryOperationID(node.BootID, ref)
	if err := e.runtime.Resume(ctx, session, runtimeOperationID); err != nil {
		markErr := e.applyTransition(ctx, clusterstate.CommandFailBootRecovery, ref)
		return errors.Join(fmt.Errorf("runtime resume: %w", err), markErr)
	}
	return e.applyTransition(ctx, clusterstate.CommandCompleteBootRecovery, ref)
}

func stableRecoveryOperationID(bootID string, ref domain.SessionRef) string {
	digest := sha256.Sum256([]byte(bootID + "\x00" + ref.Key()))
	return "boot-recovery-" + hex.EncodeToString(digest[:16])
}

func (e *Executor) applyTransition(
	ctx context.Context,
	kind clusterstate.CommandKind,
	ref domain.SessionRef,
) error {
	id, err := e.newID()
	if err != nil {
		return err
	}
	command, err := clusterstate.NewCommand(
		id,
		kind,
		e.now(),
		clusterstate.BootRecovery{Session: ref},
	)
	if err != nil {
		return err
	}
	result, err := e.apply.Apply(ctx, command)
	if err != nil {
		return err
	}
	return result.Err()
}

func operationID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
