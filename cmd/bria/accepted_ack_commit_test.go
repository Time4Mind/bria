package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

// Only one journal write fails. Actual protocol ACK, controller and subsequent
// durable writes remain real, so no full-filesystem-failure promise is implied.
type acceptedACKCommitFault struct {
	*messagejournal.Journal
	message   string
	permanent bool
	failures  atomic.Int32
}

func (j *acceptedACKCommitFault) MarkInputAccepted(ctx context.Context, session, message, owner string) (messagejournal.Input, error) {
	if message == j.message && (j.failures.Add(1) == 1 || j.permanent) {
		return messagejournal.Input{}, errors.New("synthetic acceptance commit failure")
	}
	return j.Journal.MarkInputAccepted(ctx, session, message, owner)
}

func TestModelACKSingleCommitFailureRetainsMainAndSteerOnDisk(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		for _, failed := range []string{"ack-main", "ack-steer"} {
			name := failed
			if permanent {
				name += "/permanent"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				state, sessions := acceptedProofFixture(t)
				id := sessions[0].ID()
				mode := "eof"
				if permanent {
					mode = "steer-eof"
					snapshot := sessions[0].Snapshot()
					snapshot.Binding = &domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "00000000-0000-4000-8000-000000000077", Generation: 1}
					native, err := domain.RestoreSession(snapshot)
					if err != nil {
						t.Fatal(err)
					}
					if err := state.Replace(ctx, sessions[0], native); err != nil {
						t.Fatal(err)
					}
					sessions[0] = native
				}
				starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: {Path: os.Args[0], Args: []string{"-test.run=^TestAcceptedTerminalJournalAdapterProcess$"}, Env: []string{"BRIA_TERMINAL_JOURNAL_HELPER=" + mode}}}, sessionruntime.Options{GracefulCloseTimeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				request := app.StartSessionRequest{SessionID: id, ComputerID: "local", Provider: domain.ProviderCodex, Workdir: sessions[0].Workdir(), Mode: app.SessionStartNew}
				binding, err := starter.Start(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				defer starter.Abort(context.Background(), request, binding)
				path := filepath.Join(t.TempDir(), "journal.json")
				journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				fault := &acceptedACKCommitFault{Journal: journal, message: failed, permanent: permanent}
				var clock atomic.Int64
				clock.Store(time.Now().UnixNano())
				flow, err := durableflow.New(fault, nil, nil, durableflow.Options{Owner: "ack-worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(0, clock.Load()) }})
				if err != nil {
					t.Fatal(err)
				}
				lifecycle, err := app.NewSessionTurnLifecycle(state, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				options := telegramcontroller.Options{Recovered: sessions, UIState: state, TurnLifecycle: lifecycle, DurableInput: durablecomposition.InputCustody{Flow: flow}}
				runtime := queueTerminalRuntime{Starter: starter, submitted: make(chan string, 8)}
				controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, state, runtime, archiveNotifier(func(context.Context, telegramcontroller.Notification) error { return nil }), options)
				if err != nil {
					t.Fatal(err)
				}
				defer controller.Close(context.Background())
				processor := durablecomposition.NewControllerInputProcessor(controller)
				messages := []string{"ack-main"}
				if failed == "ack-steer" {
					messages = append(messages, "ack-steer")
				}
				for _, message := range messages {
					if _, err := flow.EnqueueInput(ctx, string(id), message, []byte("synthetic "+message)); err != nil {
						t.Fatal(err)
					}
					got, processErr := flow.ProcessNextInput(ctx, string(id), processor)
					if permanent && message == failed && processErr == nil {
						t.Error("permanent acceptance commit failure lost its error")
					}
					// The original write error may remain visible, but its exact ACK
					// must not become Unknown merely because the first commit failed.
					if got.State == durableflow.InputProcessUnknown || got.State == durableflow.InputProcessDeferred {
						t.Errorf("model ACK downgraded: state=%s err=%v", got.State, processErr)
					}
				}
				if permanent {
					retained := terminalJournalInputs(t, ctx, path, id)
					submittedBeforeWake := len(runtime.submitted)
					clock.Add(int64(2 * time.Minute))
					queued, err := flow.EnqueueInput(ctx, string(id), "after-recovery-B", []byte("synthetic B"))
					if err != nil {
						t.Fatal(err)
					}
					_, _ = flow.ProcessNextInput(ctx, string(id), processor)
					if len(runtime.submitted) != submittedBeforeWake {
						t.Fatalf("expiry wake invoked provider again: before=%d after=%d", submittedBeforeWake, len(runtime.submitted))
					}
					assertRetainedRecoveryQueue(t, ctx, path, id, messages, queued.Sequence, messagejournal.InputAccepted, retained...)
				}
				if err := controller.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if permanent {
					inputs := terminalJournalInputs(t, ctx, path, id)
					for _, input := range inputs[:len(messages)] {
						if input.MessageID == failed {
							if input.Phase != messagejournal.InputPending || input.Lease.Owner != "ack-worker" {
								t.Fatalf("permanent commit failure became Unknown or intentionally requeued: %+v", input)
							}
						} else if input.Phase != messagejournal.InputAccepted {
							t.Fatalf("other ACK lost: %+v", input)
						}
					}
					// Acceptance cannot survive an unavailable durable write. Preserve
					// its error and original lease; do not claim durable Accepted here.
					if err := starter.Abort(ctx, request, binding); err != nil {
						t.Fatal(err)
					}
					assertAcceptedRestartArchiveLateFinal(t, ctx, sessions[0], path, messages)
					return
				}
				assertTerminalJournalPhase(t, ctx, path, id, messages, string(messagejournal.InputAccepted))
				if fault.failures.Load() < 2 {
					t.Fatal("exact acceptance was not retried")
				}
				if _, err := flow.EnqueueInput(ctx, string(id), "ack-B", []byte("synthetic B")); err != nil {
					t.Fatal(err)
				}
				// The replacement controller is closed for this fault-injection
				// path; verify the successor remains durable and unsubmitted.
				probe := &acceptedRestartRuntime{submitted: make(chan string, 8)}
				restarted, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, state, probe, archiveNotifier(func(context.Context, telegramcontroller.Notification) error { return nil }), options)
				if err != nil {
					t.Fatal(err)
				}
				defer restarted.Close(context.Background())
				got, err := flow.ProcessNextInput(ctx, string(id), durablecomposition.NewControllerInputProcessor(restarted))
				if err == nil || got.State != durableflow.InputProcessUnknown || len(probe.submitted) != 0 {
					t.Fatalf("closed lifecycle admitted B root: state=%s calls=%d err=%v", got.State, len(probe.submitted), err)
				}
				inputs := terminalJournalInputs(t, ctx, path, id)
				b := inputs[len(inputs)-1]
				if b.MessageID != "ack-B" || b.Phase != messagejournal.InputUnknown || b.Lease != (messagejournal.Lease{}) {
					t.Fatalf("closed lifecycle B custody changed unexpectedly: %+v", b)
				}
			})
		}
	}
}
