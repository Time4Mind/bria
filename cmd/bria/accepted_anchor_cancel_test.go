package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
)

type acceptedAnchorBarrier struct {
	*storage.SessionStore
	message          string
	entered, release chan struct{}
}

func (s *acceptedAnchorBarrier) SetCardPrompt(ctx context.Context, id domain.SessionID, message, entry string) error {
	if message == s.message && strings.HasPrefix(entry, "👨‍💻") {
		close(s.entered)
		<-s.release
	}
	return s.SessionStore.SetCardPrompt(ctx, id, message, entry)
}

type anchorCancelRuntime struct{ acceptedRestartRuntime }

func (r *anchorCancelRuntime) SubmitWithCallbacks(ctx context.Context, _ domain.SessionID, _ string, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	r.submitted <- cb.MessageID
	if err := cb.OnAccepted(cb.MessageID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	<-ctx.Done()
	return sessionruntime.TurnResult{}, ctx.Err()
}
func (r *anchorCancelRuntime) SubmitCurrentWithCallbacks(ctx context.Context, _ domain.SessionID, _ sessionruntime.StructuredInput, cb sessionruntime.TurnCallbacks) error {
	r.submitted <- cb.MessageID
	if err := cb.OnAccepted(cb.MessageID); err != nil {
		return err
	}
	return ctx.Err()
}

func TestAcceptedAnchorCancellationRetainsJournalAcrossReopen(t *testing.T) {
	for _, steer := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "steer"}[steer], func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			state, sessions := acceptedProofFixture(t)
			id := sessions[0].ID()
			path := filepath.Join(t.TempDir(), "journal.json")
			journal, err := messagejournal.Open(path, messagejournal.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "anchor-worker", LeaseDuration: time.Minute, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			barrier := &acceptedAnchorBarrier{SessionStore: state, message: "blocked-anchor", entered: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
			runtime := &anchorCancelRuntime{acceptedRestartRuntime{submitted: make(chan string, 4)}}
			c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, state, runtime, archiveNotifier(func(context.Context, telegramcontroller.Notification) error { return nil }), telegramcontroller.Options{Recovered: sessions, UIState: barrier, DurableInput: durablecomposition.InputCustody{Flow: flow}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release(); _ = c.Close(context.Background()) }()
			processor := durablecomposition.NewControllerInputProcessor(c)
			if steer {
				if _, err := flow.EnqueueInput(ctx, string(id), "root", []byte("root")); err != nil {
					t.Fatal(err)
				}
				if _, err := flow.ProcessNextInput(ctx, string(id), processor); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := flow.EnqueueInput(ctx, string(id), barrier.message, []byte("exact blocked prompt")); err != nil {
				t.Fatal(err)
			}
			processCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			type processResult struct {
				result durableflow.InputProcessResult
				err    error
			}
			done := make(chan processResult, 1)
			go func() {
				result, err := flow.ProcessNextInput(processCtx, string(id), processor)
				done <- processResult{result, err}
			}()
			select {
			case <-barrier.entered:
			case <-ctx.Done():
				t.Fatal("accepted prompt did not reach barrier")
			}
			cancel()
			var got processResult
			if !steer {
				select {
				case got = <-done:
				case <-ctx.Done():
					t.Fatal("cancelled main processing did not return")
				}
			}
			inputs := terminalJournalInputs(t, ctx, path, id)
			last := inputs[len(inputs)-1]
			if last.MessageID != barrier.message || last.Phase != messagejournal.InputAccepted {
				t.Errorf("cancel during blocked anchor lost durable ACK: %+v", last)
			}
			release()
			if steer {
				select {
				case got = <-done:
				case <-ctx.Done():
					t.Fatal("released steer processing did not return")
				}
			}
			if got.result.State != durableflow.InputProcessAccepted || !errors.Is(got.err, context.Canceled) {
				t.Errorf("cancelled accepted input downgraded: %+v %v", got.result, got.err)
			}
			if err := c.Close(ctx); err != nil {
				t.Fatal(err)
			}
			messages := []string{barrier.message}
			if steer {
				messages = append([]string{"root"}, messages...)
			}
			assertTerminalJournalPhase(t, ctx, path, id, messages, string(messagejournal.InputAccepted))
			reopened, err := storage.OpenSessionStore(filepath.Join(sessions[0].Workdir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			history, err := reopened.LoadCardHistory(ctx, id)
			if err != nil || !strings.Contains(strings.Join(history, "\n"), "👨‍💻 exact blocked prompt") {
				t.Fatalf("released exact anchor not persisted: %v %v", history, err)
			}
			want := 1
			if steer {
				want++
			}
			if len(runtime.submitted) != want {
				t.Fatal("provider input replayed")
			}
		})
	}
}
