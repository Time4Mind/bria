package nativeterminal

import (
	"context"

	"bria/internal/tmuxobserver"
)

// Observe starts a read-only event stream for the exact owned pane. The
// terminal remains the authority for proving that the socket and process still
// match the persisted binding; the observer only supplies change hints.
func (t *Terminal) Observe(ctx context.Context) (*tmuxobserver.Observer, error) {
	t.mu.Lock()
	if err := t.usable(ctx); err != nil {
		t.mu.Unlock()
		return nil, err
	}
	specification := tmuxobserver.Spec{
		Executable: t.path,
		Socket:     t.socket,
		Target:     "cli:0.0",
	}
	t.mu.Unlock()
	return tmuxobserver.Start(ctx, specification)
}
