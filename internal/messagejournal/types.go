// Package messagejournal persists the ordered hand-off of session input and
// asynchronous output. A successful enqueue is a disk acceptance boundary;
// transport acknowledgement is represented by a later phase transition.
package messagejournal

import (
	"errors"
	"time"
)

var (
	ErrConflict          = errors.New("message journal identity conflict")
	ErrInvalidFormat     = errors.New("invalid message journal format")
	ErrInvalidTransition = errors.New("invalid message journal transition")
	ErrJournalFull       = errors.New("message journal capacity reached")
	ErrLeaseOwner        = errors.New("message journal lease owner mismatch")
	ErrNoAvailable       = errors.New("no ordered journal item is available")
	ErrNotFound          = errors.New("message journal item not found")
	ErrQueueFull         = errors.New("session input queue is full")
)

type InputPhase string

const (
	InputPending   InputPhase = "pending"
	InputAccepted  InputPhase = "accepted"
	InputCompleted InputPhase = "completed"
	InputFailed    InputPhase = "failed"
	InputUnknown   InputPhase = "unknown"
)

type OutputPhase string

const (
	OutputPending   OutputPhase = "pending"
	OutputConfirmed OutputPhase = "confirmed"
	OutputFailed    OutputPhase = "failed"
	OutputUnknown   OutputPhase = "unknown"
)

// Limits bounds both accepted writes and documents loaded from disk. Limits
// are deliberately explicit so product settings can lower queue capacity
// without weakening the hard format ceilings enforced by Open.
type Limits struct {
	MaxSessions                int
	MaxInputsPerSession        int
	MaxPendingInputsPerSession int
	MaxOutputsPerSession       int
	MaxPayloadBytes            int
	MaxIDBytes                 int
	MaxKindBytes               int
	MaxReceiptBytes            int
	MaxFileBytes               int64
	MaxLeaseDuration           time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxSessions:                256,
		MaxInputsPerSession:        4096,
		MaxPendingInputsPerSession: 32,
		MaxOutputsPerSession:       4096,
		MaxPayloadBytes:            1 << 20,
		MaxIDBytes:                 256,
		MaxKindBytes:               64,
		MaxReceiptBytes:            4096,
		MaxFileBytes:               64 << 20,
		MaxLeaseDuration:           24 * time.Hour,
	}
}

type Lease struct {
	Owner string
	Until time.Time
}

type AttachmentRef struct {
	Reference string
	Size      int64
	SHA256    string
}

type Input struct {
	MessageID   string
	SessionID   string
	Sequence    uint64
	Payload     []byte
	Attachments []AttachmentRef
	Phase       InputPhase
	Lease       Lease
}

type Output struct {
	OperationID string
	SessionID   string
	Sequence    uint64
	Kind        string
	Payload     []byte
	Phase       OutputPhase
	Receipt     string
	Lease       Lease
}
