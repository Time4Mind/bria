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
	mu        sync.Mutex
	started   providerauth.StartRequest
	submitted providerauth.SubmitRequest
	state     providerauth.Status
	cancelled bool
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
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
