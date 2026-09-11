package acceptedinput

import (
	"context"
	"errors"
	"sync"
	"time"

	"bria/internal/messagejournal"
)

var ErrUncommitted = errors.New("observed input acceptance is not durably committed")

type receipt struct {
	sessionID, messageID string
	sequence             uint64
}

// Fence remembers observed ACKs only for this Flow lifetime. After restart the
// retained leased-pending journal marker must pass bootstrap reconciliation.
type Fence struct {
	mu      sync.Mutex
	blocked map[receipt]struct{}
}

// Observe is called only after the provider's full exact ACK has been validated.
func (f *Fence) Observe(sessionID, messageID string, sequence uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocked == nil {
		f.blocked = make(map[receipt]struct{})
	}
	f.blocked[receipt{sessionID, messageID, sequence}] = struct{}{}
}

func (f *Fence) clear(key receipt) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.blocked, key)
}

type JournalLeaser interface {
	JournalReader
	LeaseNextInput(context.Context, string, string, time.Time, time.Duration) (messagejournal.Input, error)
}

// Lease serializes the check and lease with observation, without changing the
// journal's normal expired-lease or accepted-skipping live-steer policy.
func (f *Fence) Lease(ctx context.Context, journal JournalLeaser, sessionID, owner string, now time.Time, duration time.Duration) (messagejournal.Input, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key := range f.blocked {
		if key.sessionID != sessionID {
			continue
		}
		input, _, err := Lookup(ctx, journal, key.sessionID, key.messageID, key.sequence)
		if err != nil {
			return messagejournal.Input{}, err
		}
		if input.Phase != messagejournal.InputAccepted && input.Phase != messagejournal.InputCompleted && input.Phase != messagejournal.InputTerminalFailed && input.Phase != messagejournal.InputSkipped {
			return messagejournal.Input{}, ErrUncommitted
		}
		delete(f.blocked, key)
	}
	return journal.LeaseNextInput(ctx, sessionID, owner, now, duration)
}
