package turnfinalization_test

import (
	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessioncloseflow"
	"bria/internal/turnfinalization"
	"context"
	"errors"
	"testing"
	"time"
)

type turns func(context.Context, domain.SessionID) (domain.Session, bool, error)

func (f turns) Finish(ctx context.Context, id domain.SessionID) (domain.Session, bool, error) {
	return f(ctx, id)
}
func (f turns) Start(context.Context, domain.SessionID) (domain.Session, error) {
	return domain.Session{}, errors.New("must not start")
}
func (f turns) BeginStop(context.Context, domain.SessionID) (domain.Session, error) {
	return domain.Session{}, errors.New("must not stop")
}

type closer func(context.Context, domain.SessionID) (app.CloseSessionResult, error)

func (f closer) Close(ctx context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
	return f(ctx, id)
}

func TestLifecycleRetryRetainsSuccessfulFinishAndCloseAcrossLocalFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	running, err := ready(t, 1).StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := running.CloseAfterWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	closing, err := scheduled.BeginClose(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	archived, err := closing.Archive(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store := &sessions{current: scheduled}
	binding, _ := scheduled.Binding()
	finishes, closes, applies := 0, 0, 0
	lifecycle := turnfinalization.Lifecycle{Turns: turns(func(context.Context, domain.SessionID) (domain.Session, bool, error) {
		finishes++
		return scheduled, true, nil
	}), Closer: closer(func(context.Context, domain.SessionID) (app.CloseSessionResult, error) {
		closes++
		if closes == 1 {
			store.set(closing)
			return app.CloseSessionResult{}, errors.New("local close receipt unavailable")
		}
		store.set(archived)
		return app.CloseSessionResult{Session: archived}, nil
	}), CloseFlow: &sessioncloseflow.Flow{}, Replace: func(domain.Session) {}, Notify: func(string, string) {}, Apply: func(context.Context, domain.Session) error {
		applies++
		if applies == 1 {
			return errors.New("local UI persistence unavailable")
		}
		return nil
	}}
	valid, err := lifecycle.Retry(ctx, scheduled.ID(), binding, "root", store, nil)
	if err != nil || !valid || finishes != 1 || closes != 2 || applies != 2 {
		t.Fatalf("lost successful finalization steps: valid=%v finish=%d close=%d apply=%d err=%v", valid, finishes, closes, applies, err)
	}
}

func TestLifecycleRetryRecognizesAmbiguousCommittedFinish(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := ready(t, 1)
	running, err := ready.StartWork(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store := &sessions{current: running}
	binding, _ := running.Binding()
	finishes := 0
	lifecycle := turnfinalization.Lifecycle{Turns: turns(func(context.Context, domain.SessionID) (domain.Session, bool, error) {
		finishes++
		store.set(ready)
		return domain.Session{}, false, errors.New("ambiguous local commit")
	}), Replace: func(domain.Session) {}, Notify: func(string, string) {}}
	valid, err := lifecycle.Retry(ctx, running.ID(), binding, "root", store, nil)
	if err != nil || !valid || finishes != 1 {
		t.Fatal("committed Finish repeated or unresolved")
	}
}
