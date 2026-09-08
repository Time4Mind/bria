package supervisioncomposition_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/controllertelemetry"
	"bria/internal/observability"
	"bria/internal/safelog"
)

func TestRecoverySourcesPersistSafeCorrelatedJSONL(t *testing.T) {
	for _, source := range []string{"manual", "startup", "live"} {
		t.Run(source, func(t *testing.T) {
			running, _ := runningSession(t)
			store := &memoryStore{session: running}
			runtime := &exitRuntime{}
			reconciler := &recoveryReconciler{unknown: true}
			if source == "manual" {
				awaiting, err := running.AwaitRecovery()
				if err != nil {
					t.Fatal(err)
				}
				store.session = awaiting
			}
			manager := recoveryManager(t, store, runtime, runtime, reconciler)
			dir := t.TempDir()
			logger, err := safelog.Open(safelog.Options{Directory: dir})
			if err != nil {
				t.Fatal(err)
			}
			observer, err := observability.NewTelegramFlowObserver(logger)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			captured := &forwardRecoveryObserver{observer: observer, done: make(chan struct{}, 8)}
			manager.SetObserver(captured)
			ctx := controllertelemetry.WithOperation(context.Background(), "private-recovery-operation")
			switch source {
			case "manual":
				_, _ = manager.RecoverSession(ctx, running.ID())
			case "startup":
				_, err = manager.RecoverStartup(ctx)
				if err != nil {
					t.Fatal(err)
				}
			case "live":
				liveCtx, cancel := context.WithCancel(ctx)
				done := make(chan error, 1)
				go func() { done <- manager.Run(liveCtx) }()
				select {
				case <-captured.done:
				case <-time.After(time.Second):
					cancel()
					t.Fatal("no live recovery outcome")
				}
				cancel()
				<-done
			}
			observer.Close()
			data, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{string(running.ID()), "private-", "thread-1", running.Workdir()} {
				if strings.Contains(string(data), private) {
					t.Fatal("recovery trace leaked private identity or payload")
				}
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != 1 {
				t.Fatalf("recovery rows=%d", len(lines))
			}
			var row safelog.Event
			if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
				t.Fatal(err)
			}
			if row.Fields["stage"] != "session.recovery_outcome" || row.Fields["selection_reason"] != source+"_recovery" || row.Result != "recovery_unknown" || row.Fields["selection_outcome"] != "recovery_unknown" {
				t.Fatalf("wrong safe recovery classification: stage=%s reason=%s result=%s", row.Fields["stage"], row.Fields["selection_reason"], row.Result)
			}
			if row.EntityID == "" || row.Fields["session_ref"] == "" || row.Fields["node_ref"] == "" || row.Fields["run_ref"] == "" {
				t.Fatal("recovery trace lost hashed correlation")
			}
		})
	}
}

type forwardRecoveryObserver struct {
	observer controllertelemetry.Observer
	done     chan struct{}
}

func (o *forwardRecoveryObserver) ObserveControllerEvent(ctx context.Context, e controllertelemetry.Event) {
	o.observer.ObserveControllerEvent(ctx, e)
	o.done <- struct{}{}
}
