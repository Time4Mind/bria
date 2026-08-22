package telegramapp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type sessionLocalStatePort struct {
	machine *clusterstate.Machine
}

func (p sessionLocalStatePort) State() *domain.State {
	return p.machine.State()
}

func (p sessionLocalStatePort) Apply(
	_ context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	return p.machine.Apply(command), nil
}

func TestSessionLocalStateIsBoundedOnWrite(t *testing.T) {
	handler := &Handler{}
	for index := 0; index < maxSessionPageStates+17; index++ {
		key := sessionPageKey{
			userID: 7, nodeID: "node", sessionID: domain.SessionID(fmt.Sprintf("session-%04d", index)),
		}
		handler.storeSessionPageState(key, cardPageState{page: index + 1})
	}
	for index := 0; index < maxPromptHashStates+19; index++ {
		key := sessionPageKey{
			userID: 7, nodeID: "node", sessionID: domain.SessionID(fmt.Sprintf("prompt-%04d", index)),
		}
		handler.storePromptHash(key, fmt.Sprintf("hash-%04d", index))
	}

	handler.sessionStateMu.Lock()
	defer handler.sessionStateMu.Unlock()
	if got := len(handler.sessionPages); got != maxSessionPageStates {
		t.Fatalf("session page entries=%d want=%d", got, maxSessionPageStates)
	}
	if got := len(handler.promptHashes); got != maxPromptHashStates {
		t.Fatalf("prompt hash entries=%d want=%d", got, maxPromptHashStates)
	}
}

func TestSessionLocalStateSweepKeepsFreshLiveAndArchivedSessions(t *testing.T) {
	now := time.Unix(1_000_000, 0).UTC()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	refs := []domain.SessionRef{
		{NodeID: "node", SessionID: "live"},
		{NodeID: "node", SessionID: "archived"},
	}
	for _, ref := range refs {
		if err := state.AddSession(domain.Session{
			ID: ref.SessionID, NodeID: ref.NodeID, OwnerID: 7, Backend: "codex",
			State: domain.SessionLive, RuntimePhase: domain.RuntimeIdle,
			CreatedAt: now, LiveSinceAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	archived := state.Sessions[refs[1].Key()]
	if err := state.ArchiveSession(
		refs[1], archived.Revision, domain.ArchiveIdle, now.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := sessionLocalStatePort{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{service: service, voicePendingState: newVoicePendingState()}
	missing := domain.SessionRef{NodeID: "node", SessionID: "deleted"}
	for _, ref := range append(refs, missing) {
		key := pageKey(7, ref)
		handler.storeSessionPageState(key, cardPageState{page: 1, pages: 1})
		handler.storePromptHash(key, "hash")
		generation := uint64(1)
		if session, ok := state.Sessions[ref.Key()]; ok {
			generation = session.RuntimeGeneration
		}
		voiceKey := voicePendingKey{userID: 7, ref: ref, generation: generation}
		handler.pendingVoices[voiceKey] = append(
			handler.pendingVoices[voiceKey],
			newPendingVoice(
				"voice-"+string(ref.SessionID), ref,
				voicePendingBaseline{ref: ref, known: true, receivedAt: now, ordinal: 1},
			),
		)
	}

	handler.sweepSessionLocalState(time.Now())
	for _, ref := range refs {
		key := pageKey(7, ref)
		if _, ok := handler.loadSessionPageState(key); !ok {
			t.Fatalf("fresh session page removed for %s", ref.Key())
		}
		if got := handler.loadPromptHash(key); got == "" {
			t.Fatalf("fresh prompt hash removed for %s", ref.Key())
		}
	}
	if _, ok := handler.loadSessionPageState(pageKey(7, missing)); ok {
		t.Fatal("deleted session page retained")
	}
	if got := handler.loadPromptHash(pageKey(7, missing)); got != "" {
		t.Fatal("deleted session prompt retained")
	}
	handler.voiceMu.Lock()
	liveVoiceKey := voicePendingKey{
		userID: 7, ref: refs[0], generation: state.Sessions[refs[0].Key()].RuntimeGeneration,
	}
	if len(handler.pendingVoices[liveVoiceKey]) != 1 {
		handler.voiceMu.Unlock()
		t.Fatal("live pending voice removed")
	}
	for _, ref := range []domain.SessionRef{refs[1], missing} {
		generation := uint64(1)
		if session, ok := state.Sessions[ref.Key()]; ok {
			generation = session.RuntimeGeneration
		}
		if len(handler.pendingVoices[voicePendingKey{
			userID: 7, ref: ref, generation: generation,
		}]) != 0 {
			handler.voiceMu.Unlock()
			t.Fatalf("unavailable pending voice retained for %s", ref.Key())
		}
	}
	handler.voiceMu.Unlock()

	live, err := service.Session(application.Principal{UserID: 7}, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ClearSession(
		application.WithOperationScope(context.Background(), "clear-voice-generation"),
		application.Principal{UserID: 7}, live,
	); err != nil {
		t.Fatal(err)
	}
	cleared, err := service.Session(application.Principal{UserID: 7}, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if handler.hasPendingVoice(
		application.Principal{UserID: 7}, refs[0], cleared.RuntimeGeneration,
	) {
		t.Fatal("old generation voice marker appeared after clear")
	}
	handler.sweepSessionLocalState(time.Now())
	handler.voiceMu.Lock()
	defer handler.voiceMu.Unlock()
	if len(handler.pendingVoices[liveVoiceKey]) != 0 {
		t.Fatal("old generation voice state survived sweep")
	}
}

func TestSessionLocalStateSweepExpiresInactiveEntries(t *testing.T) {
	now := time.Now().UTC()
	handler := &Handler{}
	key := sessionPageKey{userID: 7, nodeID: "node", sessionID: "session"}
	handler.storeSessionPageState(key, cardPageState{page: 1, pages: 1})
	handler.storePromptHash(key, "hash")

	handler.sessionStateMu.Lock()
	handler.sessionPageTouched[key] = now.Add(-sessionLocalStateTTL)
	handler.promptHashTouched[key] = now.Add(-sessionLocalStateTTL)
	handler.sessionStateMu.Unlock()
	handler.sweepSessionLocalState(now)

	if _, ok := handler.loadSessionPageState(key); ok {
		t.Fatal("expired session page retained")
	}
	if got := handler.loadPromptHash(key); got != "" {
		t.Fatal("expired prompt hash retained")
	}
}

func TestSessionLocalStateSweepRunsAtMostHourly(t *testing.T) {
	now := time.Now().UTC()
	handler := &Handler{}
	key := sessionPageKey{userID: 7, nodeID: "node", sessionID: "session"}
	handler.storePromptHash(key, "hash")
	handler.maybeSweepSessionLocalState(now)
	handler.storePromptHash(key, "hash")

	handler.sessionStateMu.Lock()
	handler.promptHashTouched[key] = now.Add(-sessionLocalStateTTL)
	handler.sessionStateMu.Unlock()
	handler.maybeSweepSessionLocalState(now.Add(sessionLocalSweepInterval - time.Second))
	if got := handler.loadPromptHash(key); got == "" {
		t.Fatal("entry swept before hourly interval")
	}
	handler.sessionStateMu.Lock()
	handler.promptHashTouched[key] = now.Add(-sessionLocalStateTTL)
	handler.sessionStateMu.Unlock()
	handler.maybeSweepSessionLocalState(now.Add(sessionLocalSweepInterval))
	if got := handler.loadPromptHash(key); got != "" {
		t.Fatal("entry retained after hourly sweep")
	}
}
