// Package processgroup isolates an adapter command and its descendants from
// Bria so the complete provider tree can be terminated without signalling the
// Bria process itself.
package processgroup

import "errors"

var (
	ErrInvalidCommand      = errors.New("invalid process command")
	ErrNotStarted          = errors.New("process command has not started")
	ErrAlreadyStarted      = errors.New("process command has already started")
	ErrUnsafeConfiguration = errors.New("process command is not in a dedicated process group")
	ErrTreeExitUnconfirmed = errors.New("process tree exit was not confirmed")
	ErrUnsupported         = errors.New("process tree isolation is unsupported on this platform")
)
