package coordinatortransfer

import (
	"context"

	"bria/internal/coordinatorbundle"
)

const SnapshotVersion = coordinatorbundle.Version

type Route = coordinatorbundle.Route
type JournalSession = coordinatorbundle.JournalSession
type TelegramScope = coordinatorbundle.TelegramScope
type Snapshot = coordinatorbundle.Bundle

type SnapshotReceipt struct {
	TransferID string
	Digest     string
}

// AtomicStateStore owns the real coordinator state activation. Stage must
// durably validate a candidate without making it active. Apply atomically
// promotes exactly that candidate. Read must return the active reread receipt.
type AtomicStateStore interface {
	Stage(context.Context, string, Snapshot) (SnapshotReceipt, error)
	Apply(context.Context, string, string) error
	Read(context.Context) (Snapshot, SnapshotReceipt, error)
}
