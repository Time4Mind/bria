package runtimehost

import (
	"context"
	"time"
)

// RuntimeDriver owns concrete operations against one host runtime. Executor
// serializes calls, validates the runtime generation, and provides deduplication.
type RuntimeDriver interface {
	SendLiteral(context.Context, string, string, string) error
	SendKey(context.Context, string, string) error
	Close(context.Context, string) error
	OpenTerminal(context.Context, string) error
	CapturePane(context.Context, string) ([]byte, error)
}

// InputConfirmer proves that the provider accepted the exact prompt after
// Bria submitted it. A missing confirmation must fail closed without blindly
// resending input because the provider may already be processing it.
type InputConfirmer interface {
	BaselineInput(context.Context, RuntimeBinding) (InputConfirmationBaseline, error)
	ConfirmInput(context.Context, RuntimeBinding, InputConfirmationBaseline, string) error
}

type InputConfirmationBaseline struct {
	ProviderSessionID string
	UserTail          []string
	CapturedAt        time.Time
	LegacyGeneration  bool
}

type RuntimeBinding struct {
	NodeID     string
	SessionID  string
	Generation uint64
	TmuxTarget string
	Backend    string
	Workdir    string
}
