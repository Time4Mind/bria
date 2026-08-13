package runtimehost

import "context"

// RuntimeDriver owns concrete operations against one host runtime. Executor
// serializes calls, validates the runtime generation, and provides deduplication.
type RuntimeDriver interface {
	SendLiteral(context.Context, string, string, string) error
	SendKey(context.Context, string, string) error
	Close(context.Context, string) error
	OpenTerminal(context.Context, string) error
	CapturePane(context.Context, string) ([]byte, error)
}

type RuntimeBinding struct {
	NodeID     string
	SessionID  string
	Generation uint64
	TmuxTarget string
	Backend    string
	Workdir    string
}
