package turnfinalization_test

import (
	"bria/internal/domain"
	"bria/internal/finalpersist"
	"bria/internal/turnfinalization"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type sessions struct {
	mu      sync.Mutex
	current domain.Session
}

func (s *sessions) Load(context.Context, domain.SessionID) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, nil
}
func (s *sessions) set(current domain.Session) { s.mu.Lock(); defer s.mu.Unlock(); s.current = current }
func ready(t *testing.T, generation uint64) domain.Session {
	t.Helper()
	s, err := domain.NewStartingSession("logical", "intent", "local", domain.ProviderCodex, "/synthetic")
	if err != nil {
		t.Fatal(err)
	}
	s, err = s.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native", Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLocalRetryUsesExactBindingAndStopsOnShutdownOrReplacement(t *testing.T) {
	for _, mode := range []string{"cancel", "stale-initial", "stale-after-failure", "ambiguous-write"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			old := ready(t, 1)
			binding, _ := old.Binding()
			store := &sessions{current: old}
			if mode == "stale-initial" {
				store.set(ready(t, 2))
			}
			attempts := 0
			durable := map[string]string{}
			err := turnfinalization.Retry(ctx, old.ID(), binding, store, func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("local persistence attempt is not bounded")
				}
				attempts++
				if mode == "ambiguous-write" {
					durable["root:final"] = "retained final"
					if attempts == 2 {
						return nil
					}
				}
				return errors.New("synthetic local save failure")
			}, func(uint64, error) {
				if mode == "cancel" {
					cancel()
				}
				if mode == "stale-after-failure" {
					store.set(ready(t, 2))
				}
			})
			switch mode {
			case "cancel":
				if !errors.Is(err, context.Canceled) || attempts != 1 {
					t.Fatal("shutdown repeated local write")
				}
			case "stale-initial":
				if !errors.Is(err, finalpersist.ErrSuperseded) || attempts != 0 {
					t.Fatal("stale binding reached persistence")
				}
			case "stale-after-failure":
				if !errors.Is(err, finalpersist.ErrSuperseded) || attempts != 1 {
					t.Fatal("replacement binding received retained final")
				}
			case "ambiguous-write":
				if err != nil || attempts != 2 || len(durable) != 1 || durable["root:final"] != "retained final" {
					t.Fatal("ambiguous retry changed exact durable identity")
				}
			}
		})
	}
}
