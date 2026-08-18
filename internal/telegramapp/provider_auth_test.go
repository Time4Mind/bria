package telegramapp_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type authServiceStub struct {
	mu            sync.Mutex
	started       providerauth.StartRequest
	submitted     providerauth.SubmitRequest
	state         providerauth.Status
	cancelled     bool
	statusStarted chan struct{}
	statusRelease chan struct{}
}

func (s *authServiceStub) Start(
	_ context.Context,
	request providerauth.StartRequest,
) (providerauth.Status, error) {
	s.mu.Lock()
	s.started = request
	status := s.state
	s.mu.Unlock()
	return status, nil
}
func (s *authServiceStub) Submit(
	_ context.Context,
	request providerauth.SubmitRequest,
) (providerauth.Status, error) {
	s.mu.Lock()
	s.submitted = request
	s.state.State = providerauth.StateSucceeded
	status := s.state
	s.mu.Unlock()
	return status, nil
}
func (s *authServiceStub) Status(context.Context, providerauth.FlowRequest) (providerauth.Status, error) {
	if s.statusStarted != nil {
		select {
		case s.statusStarted <- struct{}{}:
		default:
		}
	}
	if s.statusRelease != nil {
		<-s.statusRelease
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

func (s *authServiceStub) setState(state providerauth.Status) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}
func (s *authServiceStub) Cancel(context.Context, providerauth.FlowRequest) error {
	s.mu.Lock()
	s.cancelled = true
	s.mu.Unlock()
	return nil
}

func TestClaudeProviderAuthConsumesAndDeletesCode(t *testing.T) {
	fixture := newFixture(t)
	publishAuthBackend(t, fixture, "claude")
	auth := &authServiceStub{state: providerauth.Status{
		FlowID: "abcdefghijklmnopqrstuvwx", Backend: "claude",
		State: providerauth.StateWaitingInput, URL: "https://claude.com/auth",
		ExpiresAt: time.Now().Add(time.Minute),
	}}
	if err := fixture.handler.SetProviderAuth(auth); err != nil {
		t.Fatal(err)
	}
	token, _ := fixture.codec.Choice(
		7, telegramui.ActionProviderAuth, "provider_auth", "allowed\x00claude",
	)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 301, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID: "auth", CallbackData: encodeCallback(t, telegramui.ActionProviderAuth, token),
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 302, Kind: telegrambot.IncomingMessage, UserID: 7, ChatID: 7,
		MessageID: 11, Text: "one-time-code",
	}); err != nil {
		t.Fatal(err)
	}
	auth.mu.Lock()
	submitted := auth.submitted
	auth.mu.Unlock()
	if submitted.Code != "one-time-code" || submitted.NodeID != "allowed" {
		t.Fatalf("submitted=%+v", submitted)
	}
	if len(fixture.messenger.deleted) != 1 || fixture.messenger.deleted[0].MessageID != 11 {
		t.Fatalf("deleted=%+v", fixture.messenger.deleted)
	}
}

func TestCancelledProviderAuthCannotBeOverwrittenByStaleWatcher(t *testing.T) {
	fixture := newFixture(t)
	publishAuthBackend(t, fixture, "codex")
	statusStarted := make(chan struct{}, 1)
	statusRelease := make(chan struct{})
	auth := &authServiceStub{
		state: providerauth.Status{
			FlowID: "abcdefghijklmnopqrstuvwx", Backend: "codex",
			State: providerauth.StateWaitingUser, URL: "https://example.com/auth",
			ExpiresAt: time.Now().Add(time.Minute),
		},
		statusStarted: statusStarted, statusRelease: statusRelease,
	}
	if err := fixture.handler.SetProviderAuth(auth); err != nil {
		t.Fatal(err)
	}
	startToken, _ := fixture.codec.Choice(
		7, telegramui.ActionProviderAuth, "provider_auth", "allowed\x00codex",
	)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 401, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID: "auth", CallbackData: encodeCallback(t, telegramui.ActionProviderAuth, startToken),
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-statusStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("provider auth watcher did not poll")
	}
	challenge := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	var cancel telegramui.Callback
	for _, row := range challenge.Grid {
		for _, button := range row {
			if button.Callback.Action == telegramui.ActionProviderAuthCancel {
				cancel = button.Callback
			}
		}
	}
	if cancel.Token == "" {
		t.Fatal("provider auth challenge has no cancel action")
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 402, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID: "cancel", CallbackData: encodeCallback(t, cancel.Action, cancel.Token),
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	fixture.messenger.editNotify = make(chan struct{}, 1)
	auth.setState(providerauth.Status{
		FlowID: "abcdefghijklmnopqrstuvwx", Backend: "codex",
		State: providerauth.StateSucceeded, ExpiresAt: time.Now().Add(time.Minute),
	})
	close(statusRelease)
	select {
	case <-fixture.messenger.editNotify:
		t.Fatal("stale provider auth watcher overwrote the cancelled screen")
	case <-time.After(100 * time.Millisecond):
	}
	auth.mu.Lock()
	cancelled := auth.cancelled
	auth.mu.Unlock()
	if !cancelled {
		t.Fatal("provider auth backend was not cancelled")
	}
}

func publishAuthBackend(t *testing.T, fixture fixture, backend string) {
	t.Helper()
	command, err := clusterstate.NewCommand(
		"provider-auth-backend", clusterstate.CommandUpdateNodeRuntime, time.Now(),
		clusterstate.UpdateNodeRuntime{
			NodeID: "allowed", Status: domain.NodeOnline,
			Backends: []domain.BackendDescriptor{{Name: backend}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
}
