package nodelink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bria/internal/computer"
	"bria/internal/domain"
)

const ProtocolVersion uint16 = 1

var (
	ErrInvalidEnvelope     = errors.New("invalid node-link envelope")
	ErrIncompatibleVersion = errors.New("incompatible node-link protocol version")
	ErrUnauthenticated     = errors.New("node-link channel is not mutually authenticated")
	ErrWrongIdentity       = errors.New("node-link envelope identity does not match channel")
	ErrOperationConflict   = errors.New("operation id was reused for different content")
)

type MessageKind string

const (
	KindRegistration        MessageKind = "registration"
	KindReadiness           MessageKind = "readiness"
	KindCapabilities        MessageKind = "capabilities"
	KindCommand             MessageKind = "command"
	KindAcknowledgement     MessageKind = "acknowledgement"
	KindEvent               MessageKind = "event"
	KindHistoryRequest      MessageKind = "history_request"
	KindCoordinatorTransfer MessageKind = "coordinator_transfer"
	KindUpdate              MessageKind = "update"
)

func (kind MessageKind) valid() bool {
	switch kind {
	case KindRegistration, KindReadiness, KindCapabilities, KindCommand, KindAcknowledgement, KindEvent, KindHistoryRequest, KindCoordinatorTransfer, KindUpdate:
		return true
	default:
		return false
	}
}

func (kind MessageKind) mutating() bool {
	switch kind {
	case KindRegistration, KindReadiness, KindCapabilities, KindCommand, KindAcknowledgement, KindEvent, KindCoordinatorTransfer, KindUpdate:
		return true
	default:
		return false
	}
}

// Envelope is independent of any concrete network or TLS library.
type Envelope struct {
	Version          uint16                         `json:"version"`
	Kind             MessageKind                    `json:"kind"`
	OperationID      string                         `json:"operation_id,omitempty"`
	Generation       computer.CoordinatorGeneration `json:"coordinator_generation"`
	CoordinatorID    domain.ComputerID              `json:"coordinator_id"`
	SourceComputerID domain.ComputerID              `json:"source_computer_id"`
	TargetComputerID domain.ComputerID              `json:"target_computer_id"`
	Payload          json.RawMessage                `json:"payload,omitempty"`
}

// ChannelIdentity is supplied only after the concrete transport has verified
// both endpoint credentials. The envelope cannot authenticate itself.
type ChannelIdentity struct {
	LocalComputerID       domain.ComputerID
	PeerComputerID        domain.ComputerID
	ExecutorComputerID    domain.ComputerID
	PeerCertificateSHA256 string
	MutuallyAuthenticated bool
}

type Operation struct {
	ID     string
	Digest string
}

// OperationLedger is the persistence seam for dedupe. apply must propagate
// Operation.ID as the idempotency key whenever the external effect supports
// one. A durable implementation must expose interrupted commits for explicit
// reconciliation and must never auto-repeat an unknown effect.
type OperationLedger interface {
	ApplyOnce(ctx context.Context, operation Operation, apply func() error) (duplicate bool, err error)
}

type ProcessResult struct {
	Duplicate bool
}

type Processor struct {
	localID domain.ComputerID
	fence   *computer.Fence
	ledger  OperationLedger
}

func NewProcessor(localID domain.ComputerID, fence *computer.Fence, ledger OperationLedger) (*Processor, error) {
	if strings.TrimSpace(string(localID)) == "" || fence == nil || ledger == nil {
		return nil, ErrInvalidEnvelope
	}
	return &Processor{localID: localID, fence: fence, ledger: ledger}, nil
}

func (processor *Processor) Process(ctx context.Context, identity ChannelIdentity, envelope Envelope, apply func(context.Context, Envelope) error) (ProcessResult, error) {
	if processor == nil || apply == nil {
		return ProcessResult{}, ErrInvalidEnvelope
	}
	if envelope.Version != ProtocolVersion {
		return ProcessResult{}, ErrIncompatibleVersion
	}
	if !identity.MutuallyAuthenticated {
		return ProcessResult{}, ErrUnauthenticated
	}
	if identity.LocalComputerID != processor.localID || envelope.TargetComputerID != processor.localID || identity.PeerComputerID != envelope.SourceComputerID {
		return ProcessResult{}, ErrWrongIdentity
	}
	executorID := identity.LocalComputerID
	if identity.LocalComputerID == envelope.CoordinatorID {
		executorID = identity.PeerComputerID
	} else if identity.PeerComputerID != envelope.CoordinatorID {
		return ProcessResult{}, ErrWrongIdentity
	}
	if identity.ExecutorComputerID != executorID || executorID == envelope.CoordinatorID {
		return ProcessResult{}, ErrWrongIdentity
	}
	if err := validateEnvelope(envelope); err != nil {
		return ProcessResult{}, err
	}
	if envelope.Kind.requiresCoordinatorSource() && envelope.SourceComputerID != envelope.CoordinatorID {
		return ProcessResult{}, ErrWrongIdentity
	}
	if err := processor.fence.Validate(computer.CoordinatorTerm{CoordinatorID: envelope.CoordinatorID, Generation: envelope.Generation}); err != nil {
		return ProcessResult{}, err
	}
	if !envelope.Kind.mutating() {
		if err := apply(ctx, envelope); err != nil {
			return ProcessResult{}, err
		}
		return ProcessResult{}, nil
	}
	operation := Operation{ID: envelope.OperationID, Digest: digestEnvelope(envelope)}
	duplicate, err := processor.ledger.ApplyOnce(ctx, operation, func() error { return apply(ctx, envelope) })
	return ProcessResult{Duplicate: duplicate}, err
}

func (kind MessageKind) requiresCoordinatorSource() bool {
	switch kind {
	case KindCommand, KindHistoryRequest, KindCoordinatorTransfer, KindUpdate:
		return true
	default:
		return false
	}
}

func validateEnvelope(envelope Envelope) error {
	if !envelope.Kind.valid() || strings.TrimSpace(string(envelope.CoordinatorID)) == "" || strings.TrimSpace(string(envelope.SourceComputerID)) == "" || strings.TrimSpace(string(envelope.TargetComputerID)) == "" {
		return ErrInvalidEnvelope
	}
	if envelope.Generation == 0 {
		return computer.ErrInvalidGeneration
	}
	if envelope.Kind.mutating() && strings.TrimSpace(envelope.OperationID) == "" {
		return fmt.Errorf("%w: mutating operation id is required", ErrInvalidEnvelope)
	}
	if len(envelope.Payload) > 0 && !json.Valid(envelope.Payload) {
		return fmt.Errorf("%w: payload is not valid JSON", ErrInvalidEnvelope)
	}
	return nil
}

func digestEnvelope(envelope Envelope) string {
	encoded, _ := json.Marshal(envelope)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
