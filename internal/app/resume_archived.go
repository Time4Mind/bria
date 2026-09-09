package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"bria/internal/domain"
)

// ArchivedSessionStore is the durable boundary needed to continue one exact
// archived provider session.
type ArchivedSessionStore interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
	Replace(context.Context, domain.Session, domain.Session) error
}

// ArchivedSessionResumer continues the provider identity retained by an
// archived logical session. It never creates a replacement session as a
// fallback and leaves the archive unchanged when exact continuation fails.
// Explicit resume uses Start: native runtimes attach managed terminals or
// resume the same provider session only when it is not managed. Automatic
// recovery uses the separate Attach-only boundary.
type ArchivedSessionResumer struct {
	store    ArchivedSessionStore
	starter  SessionStarter
	lifetime domain.SessionLifetime
	now      func() time.Time
	mu       sync.Mutex
}

func NewArchivedSessionResumer(
	store ArchivedSessionStore,
	starter SessionStarter,
	lifetime domain.SessionLifetime,
	now func() time.Time,
) (*ArchivedSessionResumer, error) {
	if store == nil || starter == nil {
		return nil, errors.New("archive resume dependencies are required")
	}
	if err := domain.ValidateSessionLifetime(lifetime); err != nil {
		return nil, fmt.Errorf("validate archive resume lifetime: %w", err)
	}
	if now == nil {
		return nil, errors.New("archive resume clock is required")
	}
	return &ArchivedSessionResumer{store: store, starter: starter, lifetime: lifetime, now: now}, nil
}

func (resumer *ArchivedSessionResumer) Resume(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	if ctx == nil {
		return domain.Session{}, errors.New("archive resume context is required")
	}
	if id == "" {
		return domain.Session{}, errors.New("archived session id is required")
	}
	resumer.mu.Lock()
	defer resumer.mu.Unlock()

	archived, err := resumer.store.Load(ctx, id)
	if err != nil {
		return domain.Session{}, fmt.Errorf("load archived session: %w", err)
	}
	if archived.Status() != domain.SessionArchived {
		return domain.Session{}, fmt.Errorf("session %q is not archived", id)
	}
	prior, ok := archived.Binding()
	if !ok {
		return domain.Session{}, fmt.Errorf("archived session %q has no provider binding", id)
	}
	at := resumer.now().UTC()
	resuming, err := archived.BeginResume(at)
	if err != nil {
		return domain.Session{}, fmt.Errorf("prepare exact archive resume: %w", err)
	}
	request := StartSessionRequest{
		SessionID: archived.ID(), ComputerID: archived.ComputerID(),
		Provider: archived.Provider(), Workdir: archived.Workdir(),
		Mode: SessionStartResume, PriorBinding: &prior,
	}
	if err := request.Validate(); err != nil {
		return domain.Session{}, fmt.Errorf("validate exact archive resume: %w", err)
	}
	cleanup := resumer.starter.Abort
	cleanupAction := "abort"
	if attacher, ok := resumer.starter.(SessionAttacher); ok && attacher.SupportsAttach(archived.Provider()) {
		cleanupAction = "detach"
		cleanup = func(ctx context.Context, request StartSessionRequest, binding domain.ProviderBinding) error {
			bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			return attacher.Detach(bounded, request, binding)
		}
	}
	binding, err := resumer.starter.Start(ctx, request)
	if err != nil {
		return domain.Session{}, fmt.Errorf("resume original provider session: %w", err)
	}
	if err := ctx.Err(); err != nil && cleanupAction == "detach" {
		return domain.Session{}, errors.Join(err, wrapResumeCleanupError(cleanup(ctx, request, binding), cleanupAction))
	}
	ready, err := resuming.ResumeReady(binding, at, resumer.lifetime)
	if err != nil {
		cleanupErr := cleanup(ctx, request, binding)
		return domain.Session{}, errors.Join(
			fmt.Errorf("reject replacement provider session: %w", err),
			wrapResumeCleanupError(cleanupErr, cleanupAction),
		)
	}
	if err := resumer.store.Replace(ctx, archived, ready); err != nil {
		cleanupErr := cleanup(ctx, request, binding)
		return domain.Session{}, errors.Join(
			fmt.Errorf("persist exact archive resume: %w", err),
			wrapResumeCleanupError(cleanupErr, cleanupAction),
		)
	}
	return ready, nil
}

func wrapResumeCleanupError(err error, action string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s unpersisted resumed process: %w", action, err)
}
