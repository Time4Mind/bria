// Package nativecontrolport defines generation-bound native UI control without
// coupling consumers to the process supervisor implementation.
package nativecontrolport

import (
	"bria/internal/domain"
	"context"
)

type Request struct {
	Command, Key, ExpectedHash, ExpectedProviderSessionID string
	ExpectedGeneration                                    uint64
}
type Snapshot struct {
	Text, FullText, Hash, Model string
	Interactive                 bool
	ProviderSessionID           string
	Generation                  uint64
}
type Controller interface {
	NativeControl(context.Context, domain.SessionID, Request) (Snapshot, error)
}

// ScreenProvider only reads cached snapshots; it never waits on provider I/O.
type ScreenProvider interface {
	NativeScreen(domain.SessionID) (Snapshot, bool)
	NativeScreenUpdates() <-chan domain.SessionID
}
