// Package nativeapprovalflow gates native command approvals on current owner
// preferences and routes one-shot decisions to the exact provider process.
package nativeapprovalflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"bria/internal/domain"
	"bria/internal/nativeapproval"
	"bria/internal/nativecontrolport"
)

type Flow struct {
	once    sync.Once
	gate    chan struct{}
	claimed map[domain.SessionID]string
}

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
	_, ok = nativeapproval.ParseCodexCommandApproval(screen)
	if !ok {
		if !snapshot.Interactive && strings.TrimSpace(screen) != "" {
			delete(f.claimed, id)
		}
		return nil
	}
	// A resize changes the display fingerprint, not the pending decision.
	// Require an observed nonempty, noninteractive screen between decisions.
	key := fmt.Sprintf("%s:%d", snapshot.ProviderSessionID, snapshot.Generation)
	if f.claimed[id] == key {
		return nil
	}
	// Reread the setting after parsing. No cached flag authorizes the key.
	on, err = enabled(ctx)
	if err != nil || !on {
		return err
	}
	if f.claimed == nil {
		f.claimed = make(map[domain.SessionID]string)
	}
	f.claimed[id] = key // unknown outcomes cannot trigger automatic replay
	_, err = driver.NativeControl(ctx, id, nativecontrolport.Request{Key: "approve_once", ExpectedHash: snapshot.Hash, ExpectedProviderSessionID: snapshot.ProviderSessionID, ExpectedGeneration: snapshot.Generation})
	return err
}
