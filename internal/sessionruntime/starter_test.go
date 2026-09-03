package sessionruntime_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
)

func TestInteractiveSubmitStreamsBeforeCorrelatedQuestionResponse(t *testing.T) {
	starter, request, binding := startHelper(t, "interaction", sessionruntime.Options{})
	defer func() { _ = starter.Abort(context.Background(), request, binding) }()

	streamed := make(chan sessionruntime.TurnEvent, 1)
	interactionAccepted := make(chan sessionruntime.InteractionResponseAcceptance, 1)
	result, err := starter.SubmitWithCallbacks(context.Background(), request.SessionID, "choose", sessionruntime.TurnCallbacks{
		MessageID: "durable-message-1",
		OnAccepted: func(messageID string) error {
			if messageID != "durable-message-1" {
				t.Fatalf("accepted message id = %q", messageID)
			}
			return nil
		},
		OnEvent: func(event sessionruntime.TurnEvent) error {
			streamed <- event
			return nil
		},
		OnInteraction: func(_ context.Context, interaction sessionruntime.InteractionRequest) (sessionruntime.InteractionResponse, error) {
			select {
			case event := <-streamed:
				if event.Text != "before question" {
					t.Fatalf("streamed event = %#v", event)
				}
			default:
				t.Fatal("interaction arrived before prior commentary was streamed")
			}
			if interaction.ID != "interaction-1" || interaction.Kind != runtimeprotocol.InteractionQuestion || interaction.ItemID != "item-1" {
				t.Fatalf("interaction = %#v", interaction)
			}
			return sessionruntime.InteractionResponse{
				ID: interaction.ID, Outcome: runtimeprotocol.OutcomeAnswered,
				Answers: map[string][]string{"choice": {"First"}},
			}, nil
		},
		OnInteractionResponseAccepted: func(acceptance sessionruntime.InteractionResponseAcceptance) error {
			interactionAccepted <- acceptance
			return nil
		},
	})
	if err != nil {
		t.Fatalf("SubmitWithCallbacks() error = %v", err)
	}
	if result.Final != "selected:First" || len(result.Events) != 1 {
		t.Fatalf("result = %#v", result)
	}
	select {
	case acceptance := <-interactionAccepted:
		want := sessionruntime.InteractionResponseAcceptance{ProviderSessionID: "provider-interaction", MessageID: "durable-message-1", InteractionID: "interaction-1"}
		if acceptance != want {
			t.Fatalf("interaction response acceptance = %#v, want %#v", acceptance, want)
		}
	default:
		t.Fatal("interaction response acceptance callback was not called")
	}
}

func TestStarterVersionedJSONLFlowPreservesTextAndEventOrder(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "flow", sessionruntime.Options{})

	text := " exact text\nwith spaces "
	result, err := starter.Submit(context.Background(), request.SessionID, text)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	wantEvents := []sessionruntime.TurnEvent{
		{Kind: sessionruntime.EventCommentary, Text: "first"},
		{Kind: sessionruntime.EventQuestion, Text: "second?"},
	}
	if fmt.Sprint(result.Events) != fmt.Sprint(wantEvents) {
		t.Errorf("events = %#v, want %#v", result.Events, wantEvents)
	}
	if got, want := result.Final, "done:"+text; got != want {
		t.Errorf("final = %q, want %q", got, want)
	}
	if result.TerminalStatus != sessionruntime.StatusCompleted || result.ErrorCode != "" {
		t.Errorf("terminal = %q/%q, want completed/empty", result.TerminalStatus, result.ErrorCode)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
}

func TestStarterCarriesVerifiedLocalAttachmentOutsidePromptText(t *testing.T) {
	starter, request, binding := startHelper(t, "structured", sessionruntime.Options{})
	defer func() { _ = starter.Abort(context.Background(), request, binding) }()
	path := filepath.Join(request.Workdir, "provider-photo.png")
	result, err := starter.SubmitStructuredWithCallbacks(context.Background(), request.SessionID, sessionruntime.StructuredInput{
		Text: "inspect",
		Attachments: []sessionruntime.LocalAttachment{{
			Path: path, Size: 3,
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	}, sessionruntime.TurnCallbacks{MessageID: "telegram:photo:1"})
	if err != nil {
		t.Fatalf("SubmitStructuredWithCallbacks() error = %v", err)
	}
	if result.Final != "done:inspect" {
		t.Fatalf("result = %#v", result)
	}
}

func TestSubmitCancellationUnblocksWhenAdapterNeverReadsStdin(t *testing.T) {
	t.Parallel()
	starter, request, _ := startHelper(t, "never-read", sessionruntime.Options{
		MaxLineBytes: 2 << 20, MaxTextBytes: 1 << 20,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := starter.Submit(ctx, request.SessionID, strings.Repeat("x", 1<<20))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Submit() error = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked Submit returned after %s", elapsed)
	}
}

func TestAbortUnblocksBehindAFullSubmitPipe(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "never-read", sessionruntime.Options{
		MaxLineBytes: 2 << 20, MaxTextBytes: 1 << 20, GracefulCloseTimeout: 30 * time.Millisecond,
	})
	submitDone := make(chan error, 1)
	go func() {
		_, err := starter.Submit(context.Background(), request.SessionID, strings.Repeat("x", 1<<20))
		submitDone <- err
	}()
	time.Sleep(30 * time.Millisecond)
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	select {
	case err := <-submitDone:
		if err == nil {
			t.Fatal("blocked Submit() error = nil after Abort")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Submit remained stuck after Abort")
	}
}

func TestInvalidInterruptedTerminalKillsAdapterWithinBound(t *testing.T) {
	t.Parallel()
	starter, request, _ := startHelper(t, "bad-interrupt", sessionruntime.Options{GracefulCloseTimeout: 40 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := starter.Submit(ctx, request.SessionID, "first"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Submit() error = %v, want deadline", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := starter.Submit(context.Background(), request.SessionID, "after")
		if err != nil && !errors.Is(err, sessionruntime.ErrTurnInFlight) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("invalid interrupted terminal did not retire adapter")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewStarterRejectsUnsafeCommandAndEnvironment(t *testing.T) {
	t.Parallel()
	valid := helperCommandSpec("flow")
	tests := []struct {
		name string
		edit func(*sessionruntime.CommandSpec)
	}{
		{name: "relative exec", edit: func(spec *sessionruntime.CommandSpec) { spec.Path = filepath.Base(spec.Path) }},
		{name: "reserved session env", edit: func(spec *sessionruntime.CommandSpec) { spec.Env = append(spec.Env, "BRIA_SESSION_ID=spoof") }},
		{name: "reserved provider env", edit: func(spec *sessionruntime.CommandSpec) { spec.Env = append(spec.Env, "BRIA_PROVIDER=spoof") }},
		{name: "reserved start mode", edit: func(spec *sessionruntime.CommandSpec) {
			spec.Env = append(spec.Env, sessionruntime.EnvironmentStartMode+"=resume")
		}},
		{name: "reserved provider session", edit: func(spec *sessionruntime.CommandSpec) {
			spec.Env = append(spec.Env, sessionruntime.EnvironmentProviderSession+"=spoof")
		}},
		{name: "reserved generation", edit: func(spec *sessionruntime.CommandSpec) {
			spec.Env = append(spec.Env, sessionruntime.EnvironmentGeneration+"=999")
		}},
		{name: "reserved provider credential reference", edit: func(spec *sessionruntime.CommandSpec) {
			spec.Env = append(spec.Env, sessionruntime.EnvironmentProviderCredentialFile+"=/tmp/spoof")
		}},
		{name: "relative provider credential reference", edit: func(spec *sessionruntime.CommandSpec) {
			spec.ProviderCredentialFile = "relative/credential.json"
		}},
		{name: "duplicate env", edit: func(spec *sessionruntime.CommandSpec) { spec.Env = append(spec.Env, "BRIA_TEST_HELPER=again") }},
		{name: "invalid env", edit: func(spec *sessionruntime.CommandSpec) { spec.Env = append(spec.Env, "NOT-VALID=value") }},
		{name: "shell", edit: func(spec *sessionruntime.CommandSpec) { spec.Path = "/bin/sh" }},
		{name: "directory", edit: func(spec *sessionruntime.CommandSpec) { spec.Path = t.TempDir() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := valid
			spec.Args = append([]string(nil), valid.Args...)
			spec.Env = append([]string(nil), valid.Env...)
			test.edit(&spec)
			if _, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: spec}, sessionruntime.Options{}); err == nil {
				t.Fatal("NewStarter() error = nil")
			}
		})
	}
}

func TestStarterPassesExactProviderCredentialReferenceThroughReservedEnvironment(t *testing.T) {
	t.Parallel()
	credentialPath := filepath.Join(os.TempDir(), "bria-provider-credential-ref")
	spec := helperCommandSpec("credential-environment")
	spec.ProviderCredentialFile = credentialPath
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
		domain.ProviderCodex: spec,
	}, sessionruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest(t.TempDir(), "credential-environment")
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if binding.SessionID != credentialPath {
		t.Fatalf("provider session ID = %q, want exact credential reference %q", binding.SessionID, credentialPath)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
}

func TestStarterDoesNotFollowExecutableSymlinkRetargetedAfterConstruction(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	alias := filepath.Join(directory, "adapter")
	if err := os.Symlink(os.Args[0], alias); err != nil {
		t.Fatal(err)
	}
	spec := helperCommandSpec("flow")
	spec.Path = alias
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
		domain.ProviderCodex: spec,
	}, sessionruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", alias); err != nil {
		t.Fatal(err)
	}
	request := testRequest(t.TempDir(), "retarget")
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() followed retargeted symlink: %v", err)
	}
	if binding.SessionID != "provider-flow" {
		t.Errorf("provider session = %q, want original adapter", binding.SessionID)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
}

func TestAbortDoesNotLeakBlockedOutputReaders(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for index := range 12 {
		starter, request, binding := startHelper(t, "unsolicited", sessionruntime.Options{
			GracefulCloseTimeout: 10 * time.Millisecond, GracefulTerminateTimeout: 10 * time.Millisecond,
		})
		if err := starter.Abort(context.Background(), request, binding); err != nil {
			t.Fatalf("Abort(%d) error = %v", index, err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline+5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > baseline+5 {
		t.Fatalf("goroutines after noisy adapters = %d, baseline %d", got, baseline)
	}
}

func TestStarterFailedCompletionNeverExposesFinal(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "failure", sessionruntime.Options{})

	result, err := starter.Submit(context.Background(), request.SessionID, "hello")
	if !errors.Is(err, sessionruntime.ErrTurnFailed) {
		t.Fatalf("Submit() error = %v, want ErrTurnFailed", err)
	}
	if result.Final != "" {
		t.Errorf("failed final = %q, want empty", result.Final)
	}
	if result.TerminalStatus != sessionruntime.StatusFailed || result.ErrorCode != sessionruntime.ErrorAuthenticationFailed {
		t.Errorf("terminal = %q/%q", result.TerminalStatus, result.ErrorCode)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
}

func TestStarterRejectsConcurrentTurnAndInterruptsCancelledTurn(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "hold", sessionruntime.Options{})

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := starter.Submit(ctx, request.SessionID, "first")
		firstDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := starter.Submit(context.Background(), request.SessionID, "second"); !errors.Is(err, sessionruntime.ErrTurnInFlight) {
		t.Fatalf("concurrent Submit() error = %v, want ErrTurnInFlight", err)
	}
	cancel()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Submit() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Submit remained blocked")
	}

	deadline := time.Now().Add(time.Second)
	for {
		result, err := starter.Submit(context.Background(), request.SessionID, "after")
		if err == nil {
			if result.Final != "done:after" {
				t.Fatalf("post-interrupt final = %q", result.Final)
			}
			break
		}
		if !errors.Is(err, sessionruntime.ErrTurnInFlight) || time.Now().After(deadline) {
			t.Fatalf("post-interrupt Submit() error = %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
}

func TestCancelledSubmitWaitsForInterruptedTerminalBeforeReleasingNextTurn(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "delayed-interrupt", sessionruntime.Options{GracefulCloseTimeout: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := starter.Submit(ctx, request.SessionID, "first")
		firstDone <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancelledAt := time.Now()
	cancel()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Submit() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Submit() did not observe interrupted terminal")
	}
	if elapsed := time.Since(cancelledAt); elapsed < 60*time.Millisecond {
		t.Fatalf("cancelled Submit() returned after %s, before delayed interrupted terminal", elapsed)
	}

	result, err := starter.Submit(context.Background(), request.SessionID, "after")
	if err != nil || result.Final != "done:after" {
		t.Fatalf("next Submit() = %#v, %v; want non-colliding completed turn", result, err)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
}

func TestStopCurrentReturnsOnlyAfterCorrelatedInterruptedTerminal(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "delayed-interrupt", sessionruntime.Options{GracefulCloseTimeout: time.Second})

	turnDone := make(chan error, 1)
	go func() {
		_, err := starter.Submit(context.Background(), request.SessionID, "first")
		turnDone <- err
	}()
	time.Sleep(30 * time.Millisecond)
	stoppedAt := time.Now()
	if err := starter.StopCurrent(context.Background(), request.SessionID); err != nil {
		t.Fatalf("StopCurrent() error = %v", err)
	}
	if elapsed := time.Since(stoppedAt); elapsed < 60*time.Millisecond {
		t.Fatalf("StopCurrent() returned after %s, before delayed interrupted terminal", elapsed)
	}
	select {
	case err := <-turnDone:
		if !errors.Is(err, sessionruntime.ErrTurnFailed) {
			t.Fatalf("interrupted Submit() error = %v, want ErrTurnFailed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit() did not return after StopCurrent observed the terminal")
	}

	result, err := starter.Submit(context.Background(), request.SessionID, "after")
	if err != nil || result.Final != "done:after" {
		t.Fatalf("post-stop Submit() = %#v, %v", result, err)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
}

func TestWaitIsBindingAwareAndSignalsConfirmedProcessExit(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "flow", sessionruntime.Options{})
	waitDone := make(chan error, 1)
	go func() { waitDone <- starter.Wait(context.Background(), request.SessionID, binding) }()
	select {
	case err := <-waitDone:
		t.Fatalf("Wait() returned before process exit: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait() did not report confirmed process exit")
	}
	if err := starter.Wait(context.Background(), request.SessionID, binding); err != nil {
		t.Fatalf("Wait() on matching tombstone = %v", err)
	}
	stale := binding
	stale.Generation++
	if err := starter.Wait(context.Background(), request.SessionID, stale); !errors.Is(err, sessionruntime.ErrBindingMismatch) {
		t.Fatalf("Wait(stale binding) = %v, want ErrBindingMismatch", err)
	}
}

func TestWaitSignalsUnexpectedAdapterExitToSupervisor(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "exit-on-submit", sessionruntime.Options{})
	waitDone := make(chan error, 1)
	go func() { waitDone <- starter.Wait(context.Background(), request.SessionID, binding) }()
	if _, err := starter.Submit(context.Background(), request.SessionID, "trigger exit"); err == nil {
		t.Fatal("Submit() error = nil after unexpected adapter exit")
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait() after unexpected exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait() did not signal unexpected adapter exit")
	}
	if err := starter.Wait(context.Background(), request.SessionID, binding); err != nil {
		t.Fatalf("Wait() lost exited generation tombstone: %v", err)
	}
}

func TestStarterRejectsUncorrelatedResponseAndStopsAdapter(t *testing.T) {
	t.Parallel()
	starter, request, _ := startHelper(t, "uncorrelated", sessionruntime.Options{})
	if _, err := starter.Submit(context.Background(), request.SessionID, "hello"); !errors.Is(err, sessionruntime.ErrProtocol) {
		t.Fatalf("Submit() error = %v, want ErrProtocol", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := starter.Submit(context.Background(), request.SessionID, "again")
		if err != nil && !errors.Is(err, sessionruntime.ErrTurnInFlight) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("adapter still accepted work after correlation failure")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStarterRejectsOversizedTextBeforeAdapter(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "flow", sessionruntime.Options{MaxTextBytes: 8})
	if _, err := starter.Submit(context.Background(), request.SessionID, "123456789"); !errors.Is(err, sessionruntime.ErrTextTooLarge) {
		t.Fatalf("Submit() error = %v, want ErrTextTooLarge", err)
	}
	result, err := starter.Submit(context.Background(), request.SessionID, "ok")
	if err != nil || result.Final != "done:ok" {
		t.Fatalf("valid Submit() = %#v, %v", result, err)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
}

func TestStarterRejectsNonProtocolReadinessAndCleansReservation(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	starter := newHelperStarter(t, "bad-ready", sessionruntime.Options{})
	request := testRequest(workdir, "bad-ready")
	if _, err := starter.Start(context.Background(), request); !errors.Is(err, sessionruntime.ErrProtocol) {
		t.Fatalf("Start() error = %v, want ErrProtocol", err)
	}
}

func TestStarterRejectsOversizedReadinessLine(t *testing.T) {
	t.Parallel()
	starter := newHelperStarter(t, "oversized-ready", sessionruntime.Options{MaxLineBytes: 256})
	if _, err := starter.Start(context.Background(), testRequest(t.TempDir(), "oversized-ready")); !errors.Is(err, sessionruntime.ErrProtocol) {
		t.Fatalf("Start() error = %v, want ErrProtocol", err)
	}
}

func TestCancelledReadinessWaitKillsChildAndReleasesReservation(t *testing.T) {
	t.Parallel()
	starter := newHelperStarter(t, "delayed-ready", sessionruntime.Options{HandshakeTimeout: time.Second})
	request := testRequest(t.TempDir(), "delayed-ready")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := starter.Start(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled Start() error = %v", err)
	}
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("second Start() error = %v, want released reservation", err)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
}

func TestStarterUsesExactWorkdirAndIsolatedEnvironment(t *testing.T) {
	t.Setenv("BRIA_UNRELATED_PARENT_SECRET", "must-not-reach-child")
	workdir := t.TempDir()
	starter := newHelperStarter(t, "environment", sessionruntime.Options{})
	request := testRequest(workdir, "environment")
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	wantWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if binding.SessionID != wantWorkdir {
		t.Errorf("provider session = %q, want exact cwd %q", binding.SessionID, wantWorkdir)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
}

func TestStartTransportsExplicitNewAndResumeContextAndPersistsGeneration(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	starter := newHelperStarter(t, "startup-contract", sessionruntime.Options{})
	request := testRequest(workdir, "startup-new")
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start(new) error = %v", err)
	}
	if binding.SessionID != "provider-new" || binding.Generation != 1 {
		t.Fatalf("Start(new) binding = %#v, want provider-new generation 1", binding)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}

	// A fresh Starter has no in-memory tombstone. The persisted binding in the
	// request is therefore the sole generation and exact-resume source.
	fresh := newHelperStarter(t, "startup-contract", sessionruntime.Options{})
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-persisted", Generation: 7}
	resume := testRequest(workdir, "startup-resume")
	resume.Mode = app.SessionStartResume
	resume.PriorBinding = &prior
	resumed, err := fresh.Start(context.Background(), resume)
	if err != nil {
		t.Fatalf("Start(resume) error = %v", err)
	}
	if resumed.SessionID != prior.SessionID || resumed.Generation != prior.Generation+1 {
		t.Fatalf("Start(resume) binding = %#v, want exact session and generation 8", resumed)
	}
	if err := fresh.Abort(context.Background(), resume, resumed); err != nil {
		t.Fatal(err)
	}
}

func TestResumeRejectsDifferentReadySessionWithoutReplacingPersistedBinding(t *testing.T) {
	t.Parallel()
	starter := newHelperStarter(t, "resume-mismatch", sessionruntime.Options{})
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-persisted", Generation: 4}
	request := testRequest(t.TempDir(), "resume-mismatch")
	request.Mode = app.SessionStartResume
	request.PriorBinding = &prior
	if _, err := starter.Start(context.Background(), request); !errors.Is(err, sessionruntime.ErrProtocol) {
		t.Fatalf("Start(resume mismatch) error = %v, want ErrProtocol", err)
	}
	if err := starter.Wait(context.Background(), request.SessionID, prior); !errors.Is(err, sessionruntime.ErrSessionNotTracked) {
		t.Fatalf("failed resume invented a tracked binding: Wait() = %v", err)
	}
}

func TestStartRejectsAmbiguousOrInvalidResumeContextBeforeLaunching(t *testing.T) {
	t.Parallel()
	starter := newHelperStarter(t, "startup-contract", sessionruntime.Options{})
	validPrior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-persisted", Generation: 3}
	tests := []struct {
		name string
		edit func(*app.StartSessionRequest)
	}{
		{name: "mode absent", edit: func(request *app.StartSessionRequest) { request.Mode = "" }},
		{name: "mode unknown", edit: func(request *app.StartSessionRequest) { request.Mode = "replace" }},
		{name: "new with prior", edit: func(request *app.StartSessionRequest) { request.PriorBinding = &validPrior }},
		{name: "resume without prior", edit: func(request *app.StartSessionRequest) { request.Mode = app.SessionStartResume }},
		{name: "resume provider mismatch", edit: func(request *app.StartSessionRequest) {
			request.Mode = app.SessionStartResume
			prior := validPrior
			prior.Provider = domain.ProviderClaude
			request.PriorBinding = &prior
		}},
		{name: "resume empty provider id", edit: func(request *app.StartSessionRequest) {
			request.Mode = app.SessionStartResume
			prior := validPrior
			prior.SessionID = " "
			request.PriorBinding = &prior
		}},
		{name: "resume zero generation", edit: func(request *app.StartSessionRequest) {
			request.Mode = app.SessionStartResume
			prior := validPrior
			prior.Generation = 0
			request.PriorBinding = &prior
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := testRequest(t.TempDir(), "invalid-"+strings.ReplaceAll(test.name, " ", "-"))
			test.edit(&request)
			if _, err := starter.Start(context.Background(), request); err == nil {
				t.Fatal("Start() error = nil")
			}
		})
	}
}

func TestAbortSendsCloseThenKillsAdapterAfterGracePeriod(t *testing.T) {
	t.Parallel()
	starter, request, binding := startHelper(t, "ignore-close", sessionruntime.Options{GracefulCloseTimeout: 50 * time.Millisecond})
	started := time.Now()
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond || elapsed > time.Second {
		t.Errorf("Abort duration = %s, want bounded graceful wait", elapsed)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("second Abort() error = %v", err)
	}
}

func TestStarterAllowsOneProcessAndRestartsAtNextGeneration(t *testing.T) {
	t.Parallel()
	starter := newHelperStarter(t, "flow", sessionruntime.Options{})
	request := testRequest(t.TempDir(), "generation")
	first, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := starter.Start(context.Background(), request); !errors.Is(err, sessionruntime.ErrSessionAlreadyTracked) {
		t.Fatalf("duplicate Start() error = %v", err)
	}
	if err := starter.Abort(context.Background(), request, first); err != nil {
		t.Fatal(err)
	}
	resume := resumeRequest(request, first)
	second, err := starter.Start(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != first.Generation+1 {
		t.Errorf("generation = %d, want %d", second.Generation, first.Generation+1)
	}
	if err := starter.Abort(context.Background(), request, first); !errors.Is(err, sessionruntime.ErrBindingMismatch) {
		t.Errorf("Abort(old binding) error = %v", err)
	}
	if err := starter.Abort(context.Background(), request, second); err != nil {
		t.Fatal(err)
	}
}

func TestAbortRacesNaturalExitWithoutConcurrentCmdWait(t *testing.T) {
	t.Parallel()
	for index := range 50 {
		starter := newHelperStarter(t, "exit-after-ready", sessionruntime.Options{
			GracefulCloseTimeout:     5 * time.Millisecond,
			GracefulTerminateTimeout: 5 * time.Millisecond,
		})
		request := testRequest(t.TempDir(), fmt.Sprintf("exit-race-%d", index))
		binding, err := starter.Start(context.Background(), request)
		if err != nil {
			continue
		}
		if err := starter.Abort(context.Background(), request, binding); err != nil {
			t.Fatalf("Abort(%d) error = %v", index, err)
		}
	}
}

func TestFailedReadinessAttemptsPreservePriorGeneration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		behavior  string
		start     func(*sessionruntime.Starter, app.StartSessionRequest) error
		wantError error
	}{
		{
			name: "invalid ready", behavior: "bad-ready", wantError: sessionruntime.ErrProtocol,
			start: func(starter *sessionruntime.Starter, request app.StartSessionRequest) error {
				_, err := starter.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "timeout", behavior: "hang", wantError: nil,
			start: func(starter *sessionruntime.Starter, request app.StartSessionRequest) error {
				_, err := starter.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "cancel", behavior: "delayed", wantError: context.DeadlineExceeded,
			start: func(starter *sessionruntime.Starter, request app.StartSessionRequest) error {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
				defer cancel()
				_, err := starter.Start(ctx, request)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workdir := t.TempDir()
			starter := newHelperStarter(t, "dynamic", sessionruntime.Options{HandshakeTimeout: 300 * time.Millisecond})
			request := testRequest(workdir, "dynamic")
			first := startAndAbort(t, starter, request)
			request = resumeRequest(request, first)
			if err := os.WriteFile(filepath.Join(workdir, "behavior"), []byte(test.behavior), 0o600); err != nil {
				t.Fatal(err)
			}
			err := test.start(starter, request)
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("failed attempt error = %v, want %v", err, test.wantError)
				}
			} else if err == nil {
				t.Fatal("failed attempt error = nil")
			}
			if err := starter.Abort(context.Background(), request, first); err != nil {
				t.Fatalf("prior tombstone lost after failed attempt: %v", err)
			}
			if err := os.WriteFile(filepath.Join(workdir, "behavior"), []byte("good"), 0o600); err != nil {
				t.Fatal(err)
			}
			second, err := starter.Start(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if second.Generation != first.Generation+1 {
				t.Errorf("generation after failed attempt = %d, want %d", second.Generation, first.Generation+1)
			}
			if err := starter.Abort(context.Background(), request, first); !errors.Is(err, sessionruntime.ErrBindingMismatch) {
				t.Errorf("stale Abort() error = %v, want ErrBindingMismatch", err)
			}
			if err := starter.Abort(context.Background(), request, second); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCommandStartFailurePreservesPriorGeneration(t *testing.T) {
	t.Parallel()
	spec := helperCommandSpec("flow")
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
		domain.ProviderCodex: spec,
	}, sessionruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	request := testRequest(workdir, "start-error")
	first := startAndAbort(t, starter, request)
	request = resumeRequest(request, first)
	if err := os.Remove(workdir); err != nil {
		t.Fatal(err)
	}
	if _, err := starter.Start(context.Background(), request); err == nil {
		t.Fatal("Start() error = nil for removed workdir")
	}
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != first.Generation+1 {
		t.Errorf("generation = %d, want %d", second.Generation, first.Generation+1)
	}
	if err := starter.Abort(context.Background(), request, first); !errors.Is(err, sessionruntime.ErrBindingMismatch) {
		t.Errorf("stale Abort() error = %v", err)
	}
	if err := starter.Abort(context.Background(), request, second); err != nil {
		t.Fatal(err)
	}
}

func startAndAbort(t *testing.T, starter *sessionruntime.Starter, request app.StartSessionRequest) domain.ProviderBinding {
	t.Helper()
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
	return binding
}

func startHelper(t *testing.T, mode string, options sessionruntime.Options) (*sessionruntime.Starter, app.StartSessionRequest, domain.ProviderBinding) {
	t.Helper()
	request := testRequest(t.TempDir(), mode)
	starter := newHelperStarter(t, mode, options)
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return starter, request, binding
}

func newHelperStarter(t *testing.T, mode string, options sessionruntime.Options) *sessionruntime.Starter {
	t.Helper()
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
		domain.ProviderCodex: helperCommandSpec(mode),
	}, options)
	if err != nil {
		t.Fatalf("NewStarter() error = %v", err)
	}
	return starter
}

func helperCommandSpec(mode string) sessionruntime.CommandSpec {
	return sessionruntime.CommandSpec{
		Path: os.Args[0],
		Args: []string{"-test.run=TestSessionRuntimeHelperProcess", "--", mode},
		Env:  []string{"BRIA_TEST_HELPER=1", "BRIA_ALLOWED_ADAPTER_CONFIG=allowed"},
	}
}

func testRequest(workdir, suffix string) app.StartSessionRequest {
	return app.StartSessionRequest{
		SessionID: domain.SessionID("logical-" + suffix), ComputerID: "local",
		Provider: domain.ProviderCodex, Workdir: workdir, Mode: app.SessionStartNew,
	}
}

func resumeRequest(request app.StartSessionRequest, prior domain.ProviderBinding) app.StartSessionRequest {
	request.Mode = app.SessionStartResume
	request.PriorBinding = &prior
	return request
}

type parentMessage struct {
	Protocol            int                                  `json:"protocol"`
	Type                string                               `json:"type"`
	RequestID           string                               `json:"request_id"`
	Text                string                               `json:"text"`
	MessageID           string                               `json:"message_id"`
	Attachments         []runtimeprotocol.LocalAttachment    `json:"attachments"`
	InteractionResponse *runtimeprotocol.InteractionResponse `json:"interaction_response"`
}

func TestSessionRuntimeHelperProcess(t *testing.T) {
	if os.Getenv("BRIA_TEST_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	behavior := "good"
	if mode == "dynamic" {
		if raw, err := os.ReadFile("behavior"); err == nil {
			behavior = string(raw)
		}
		if behavior == "hang" {
			for {
				time.Sleep(time.Hour)
			}
		}
		if behavior == "delayed" {
			time.Sleep(150 * time.Millisecond)
		}
	}
	if mode == "oversized-ready" {
		fmt.Println(strings.Repeat("x", 1024))
		return
	}
	if mode == "delayed-ready" {
		time.Sleep(150 * time.Millisecond)
	}
	authentication := "unknown"
	if mode == "bad-ready" || behavior == "bad-ready" {
		authentication = "confirmed"
	}
	providerSessionID := "provider-" + mode
	if mode == "startup-contract" || mode == "resume-mismatch" {
		startMode := os.Getenv("BRIA_START_MODE")
		priorID := os.Getenv("BRIA_PROVIDER_SESSION_ID")
		generation := os.Getenv("BRIA_GENERATION")
		switch startMode {
		case "new":
			if priorID != "" || generation != "1" {
				os.Exit(36)
			}
			providerSessionID = "provider-new"
		case "resume":
			if priorID != "provider-persisted" || generation != "8" && mode == "startup-contract" {
				os.Exit(37)
			}
			providerSessionID = priorID
			if mode == "resume-mismatch" {
				providerSessionID = "provider-replacement"
			}
		default:
			os.Exit(38)
		}
	}
	if mode == "environment" {
		if os.Getenv("BRIA_UNRELATED_PARENT_SECRET") != "" || os.Getenv("BRIA_ALLOWED_ADAPTER_CONFIG") != "allowed" || os.Getenv("BRIA_SESSION_ID") != "logical-environment" || os.Getenv("BRIA_PROVIDER") != "codex" {
			os.Exit(31)
		}
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(32)
		}
		providerSessionID = cwd
	}
	if mode == "credential-environment" {
		providerSessionID = os.Getenv(sessionruntime.EnvironmentProviderCredentialFile)
	}
	emit(map[string]any{
		"protocol": 1, "type": "ready", "provider_session_id": providerSessionID,
		"readiness": "protocol", "authentication": authentication,
	})
	if mode == "exit-after-ready" {
		time.Sleep(time.Millisecond)
		return
	}
	if mode == "never-read" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "unsolicited" {
		for range 100 {
			emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": "noise"})
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "grandchild" {
		startGrandchildAdapter()
	}
	if mode == "bad-ready" {
		for {
			time.Sleep(time.Hour)
		}
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message parentMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.Protocol != 1 {
			os.Exit(33)
		}
		switch message.Type {
		case "close":
			if mode == "ignore-close" {
				for {
					time.Sleep(time.Hour)
				}
			}
			return
		case "interrupt":
			if mode == "bad-interrupt" {
				emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "completed"})
				continue
			}
			if mode == "delayed-interrupt" {
				time.Sleep(75 * time.Millisecond)
			}
			emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "interrupted", "error_code": "interrupted"})
		case "submit":
			switch mode {
			case "structured":
				if message.Text != "inspect" || message.MessageID != "telegram:photo:1" || len(message.Attachments) != 1 ||
					!filepath.IsAbs(message.Attachments[0].Path) || filepath.Base(message.Attachments[0].Path) != "provider-photo.png" ||
					strings.Contains(message.Text, message.Attachments[0].Path) {
					os.Exit(41)
				}
				emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID, "message_id": message.MessageID})
				emit(map[string]any{"protocol": 1, "type": "final", "request_id": message.RequestID, "text": "done:" + message.Text})
				emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "completed"})
			case "interaction":
				emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID, "message_id": message.MessageID})
				emit(map[string]any{"protocol": 1, "type": "event", "request_id": message.RequestID, "kind": "commentary", "text": "before question"})
				emit(map[string]any{"protocol": 1, "type": "interaction_request", "request_id": message.RequestID, "interaction_request": map[string]any{
					"id": "interaction-1", "kind": "question", "thread_id": "thread-1", "turn_id": "turn-1", "item_id": "item-1", "blocking": true,
					"questions": []map[string]any{{"id": "choice", "header": "Pick", "text": "Pick one", "options": []map[string]any{{"label": "First"}}}},
				}})
			case "uncorrelated":
				emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": "wrong"})
			case "exit-on-submit":
				emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID})
				return
			case "failure":
				emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID})
				emit(map[string]any{"protocol": 1, "type": "final", "request_id": message.RequestID, "text": "must not publish"})
				emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "failed", "error_code": "authentication_failed"})
			case "hold", "bad-interrupt", "delayed-interrupt":
				if message.Text == "first" {
					emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID})
					continue
				}
				success(message)
			default:
				success(message)
			}
		case "interaction_response":
			if mode != "interaction" || message.RequestID == "" || message.InteractionResponse == nil ||
				message.InteractionResponse.ID != "interaction-1" || message.InteractionResponse.Answers["choice"][0] != "First" {
				os.Exit(39)
			}
			emit(map[string]any{"protocol": 1, "type": "interaction_response_accepted", "provider_session_id": providerSessionID, "request_id": message.RequestID, "message_id": "durable-message-1", "interaction_id": "interaction-1"})
			emit(map[string]any{"protocol": 1, "type": "final", "request_id": message.RequestID, "text": "selected:First"})
			emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "completed"})
		case "reconcile_accepted_turns":
			if mode != "reconcile" || message.RequestID == "" {
				os.Exit(40)
			}
			emit(map[string]any{"protocol": 1, "type": "accepted_turn", "request_id": message.RequestID, "message_id": "m-complete", "status": "completed"})
			emit(map[string]any{"protocol": 1, "type": "accepted_turn", "request_id": message.RequestID, "message_id": "m-live", "status": "unknown"})
			emit(map[string]any{"protocol": 1, "type": "reconciliation_completed", "request_id": message.RequestID})
		default:
			os.Exit(34)
		}
	}
}

func success(message parentMessage) {
	emit(map[string]any{"protocol": 1, "type": "accepted", "request_id": message.RequestID})
	emit(map[string]any{"protocol": 1, "type": "event", "request_id": message.RequestID, "kind": "commentary", "text": "first"})
	emit(map[string]any{"protocol": 1, "type": "event", "request_id": message.RequestID, "kind": "question", "text": "second?"})
	emit(map[string]any{"protocol": 1, "type": "final", "request_id": message.RequestID, "text": "done:" + message.Text})
	emit(map[string]any{"protocol": 1, "type": "completed", "request_id": message.RequestID, "status": "completed"})
}

var outputMu sync.Mutex

func emit(message map[string]any) {
	outputMu.Lock()
	defer outputMu.Unlock()
	if json.NewEncoder(os.Stdout).Encode(message) != nil {
		os.Exit(35)
	}
}
