// Package nativeapprovalflow gates native command approvals on current owner
// preferences and routes one-shot decisions to the exact provider process.
package nativeapprovalflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"bria/internal/domain"
	"bria/internal/nativeapproval"
	"bria/internal/nativecontrolport"
)

type Flow struct {
	Observer  Observer
	once      sync.Once
	gate      chan struct{}
	claimed   map[domain.SessionID]approvalClaim
	uncertain map[domain.SessionID]uncertainClaims
}

type Outcome string

const (
	Confirmed Outcome = "confirmed"
	Stale     Outcome = "stale"
	Uncertain Outcome = "uncertain"
)

type Event struct {
	SessionID, ProviderSessionID, Fingerprint string
	Generation                                uint64
	Outcome                                   Outcome
}

type Observer interface{ ObserveNativeApproval(context.Context, Event) }

type approvalClaim struct {
	generation  string
	fingerprint string
	hash        string
	outcome     approvalOutcome
}

type uncertainClaims struct {
	generation string
	identities map[string]struct{}
}

type approvalOutcome uint8

const (
	approvalConfirmed approvalOutcome = iota + 1
	approvalStale
	approvalUncertain
)

func (f *Flow) lock(ctx context.Context) error {
	f.once.Do(func() { f.gate = make(chan struct{}, 1); f.gate <- struct{}{} })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.gate:
		if err := ctx.Err(); err != nil {
			f.unlock()
			return err
		}
		return nil
	}
}
func (f *Flow) unlock() { f.gate <- struct{}{} }

// Toggle and Observe share one linearization point: after disabling completes,
// no earlier automatic key remains queued by this flow.
func (f *Flow) Toggle(ctx context.Context, toggle func(context.Context) error) error {
	if err := f.lock(ctx); err != nil {
		return err
	}
	defer f.unlock()
	return toggle(ctx)
}

func (f *Flow) Observe(ctx context.Context, id domain.SessionID, source nativecontrolport.ScreenProvider, driver nativecontrolport.Controller, enabled func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := f.lock(ctx); err != nil {
		return err
	}
	defer f.unlock()
	if source == nil || driver == nil || enabled == nil {
		return nil
	}
	on, err := enabled(ctx)
	if err != nil || !on {
		return err
	}
	snapshot, ok := source.NativeScreen(id)
	if !ok || snapshot.Hash == "" || snapshot.ProviderSessionID == "" {
		return nil
	}
	screen := snapshot.FullText
	if screen == "" {
		screen = snapshot.Text
	}
	request, ok := nativeapproval.ParseCodexCommandApproval(screen)
	if !ok {
		if !snapshot.Interactive && strings.TrimSpace(screen) != "" {
			if previous, exists := f.claimed[id]; !exists || previous.outcome != approvalUncertain {
				delete(f.claimed, id)
			}
		}
		return nil
	}
	// Distinct command dialogs can replace each other without an observable
	// noninteractive frame when Codex starts parallel tools. Bind the claim to
	// the parsed dialog too, so the next command is not mistaken for a replay.
	generation := fmt.Sprintf("%s:%d", snapshot.ProviderSessionID, snapshot.Generation)
	identity := request.DecisionID
	if uncertain, exists := f.uncertain[id]; exists {
		if uncertain.generation != generation {
			delete(f.uncertain, id)
		} else if _, blocked := uncertain.identities[identity]; blocked {
			return nil
		}
	}
	if previous, exists := f.claimed[id]; exists && previous.generation == generation {
		if previous.outcome == approvalConfirmed && previous.fingerprint == request.Fingerprint ||
			previous.outcome == approvalStale && previous.fingerprint == request.Fingerprint && previous.hash == snapshot.Hash {
			return nil
		}
	}
	// Reread the setting after parsing. No cached flag authorizes the key.
	on, err = enabled(ctx)
	if err != nil || !on {
		return err
	}
	if f.claimed == nil {
		f.claimed = make(map[domain.SessionID]approvalClaim)
	}
	_, err = driver.NativeControl(ctx, id, nativecontrolport.Request{Key: "approve_once", ExpectedHash: snapshot.Hash, ExpectedProviderSessionID: snapshot.ProviderSessionID, ExpectedGeneration: snapshot.Generation})
	outcome := approvalConfirmed
	telemetryOutcome := Confirmed
	if errors.Is(err, nativecontrolport.ErrStale) {
		outcome = approvalStale
		telemetryOutcome = Stale
	} else if err != nil {
		outcome = approvalUncertain
		telemetryOutcome = Uncertain
		if f.uncertain == nil {
			f.uncertain = make(map[domain.SessionID]uncertainClaims)
		}
		uncertain := f.uncertain[id]
		if uncertain.generation != generation {
			uncertain = uncertainClaims{generation: generation, identities: make(map[string]struct{})}
		}
		uncertain.identities[identity] = struct{}{}
		f.uncertain[id] = uncertain
	}
	f.claimed[id] = approvalClaim{generation: generation, fingerprint: request.Fingerprint, hash: snapshot.Hash, outcome: outcome}
	if f.Observer != nil {
		f.Observer.ObserveNativeApproval(ctx, Event{
			SessionID: string(id), ProviderSessionID: snapshot.ProviderSessionID, Fingerprint: request.Fingerprint,
			Generation: snapshot.Generation, Outcome: telemetryOutcome,
		})
	}
	return err
}
