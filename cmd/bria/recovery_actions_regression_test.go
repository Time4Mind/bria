package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/telegramui"
)

func TestRecoveryPreparedRefreshCannotRouteToNativeScreen(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	codec, err := callbacktoken.New(bytes.Repeat([]byte{42}, 32), nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input := telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "RECOVERY_HISTORY", Anchors: []string{"history:1"}}}, View: telegramui.PageView{Page: 1, Pages: 1}, Keyboard: telegramui.CardKeyboardInput{View: telegramui.PageView{Page: 1, Pages: 1}, Recovery: true}}
	prepared, err := telegramflow.PrepareCardRefresh("recovery-refresh", "11111111-1111-4111-9111-111111111111", 42, 99, input, "header", false, nil, presenter)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Card.ScreenEligible || prepared.Status.ScreenSessionID != "" {
		t.Fatalf("recovery refresh routes history to native Screen: eligible=%t session=%s", prepared.Card.ScreenEligible, prepared.Status.ScreenSessionID)
	}
}

type recoveryActionFunc func(context.Context, domain.SessionID) (domain.Session, error)

func (f recoveryActionFunc) RecoverSession(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	return f(ctx, id)
}

type recoverySubmitProbe struct{ calls atomic.Int32 }

func (s *recoverySubmitProbe) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	s.calls.Add(1)
	return sessionruntime.TurnResult{}, fmt.Errorf("unexpected replay")
}

func TestManualRecoveryActionsStayResponsiveAndPreserveOutcome(t *testing.T) {
	for _, scenario := range []struct {
		name                         string
		success, switchAway, persist bool
	}{
		{"unknown", false, false, false}, {"proven_final", true, false, true},
		{"proven_final_after_switch", true, true, true}, {"unpersisted_success", true, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111", "22222222-2222-4222-9222-222222222222")
			lost, other := sessions[0], sessions[1]
			awaiting, err := lost.AwaitRecovery()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Replace(ctx, lost, awaiting); err != nil {
				t.Fatal(err)
			}
			if err := store.SetActiveSession(ctx, lost.ID()); err != nil {
				t.Fatal(err)
			}
			if err := store.InsertCardTypedHistoryAfterPrompt(ctx, lost.ID(), "prompt-"+string(lost.ID()), "BEFORE_RECOVERY", "commentary"); err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}, 16), make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			var attempts atomic.Int32
			notices := make(chan telegramcontroller.Notification, 16)
			probe := &recoverySubmitProbe{}
			recoverer := recoveryActionFunc(func(ctx context.Context, id domain.SessionID) (domain.Session, error) {
				attempts.Add(1)
				started <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return domain.Session{}, ctx.Err()
				}
				if id != lost.ID() {
					return domain.Session{}, fmt.Errorf("wrong recovery target")
				}
				if !scenario.success {
					return awaiting, nil
				}
				binding, _ := awaiting.Binding()
				binding.Generation++
				ready, err := awaiting.Recovered(binding, time.Now())
				if err != nil {
					return domain.Session{}, err
				}
				if scenario.persist {
					if err := store.InsertCardTypedHistoryAfterPrompt(ctx, id, "prompt-"+string(id), "EXACT_RECOVERED_FINAL", "final"); err != nil {
						return domain.Session{}, err
					}
					if err := store.Replace(ctx, awaiting, ready); err != nil {
						return domain.Session{}, err
					}
				}
				return ready, nil
			})
			c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, probe, archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error { notices <- n; return nil }), telegramcontroller.Options{Recovered: []domain.Session{other}, UIState: store, Recoverer: recoverer})
			if err != nil {
				finish()
				t.Fatal(err)
			}
			defer func() { finish(); _ = c.Close(ctx) }()
			call := func(action telegramcontroller.SemanticAction) telegramcontroller.SemanticActionResult {
				t.Helper()
				type outcome struct {
					result telegramcontroller.SemanticActionResult
					err    error
				}
				done := make(chan outcome, 1)
				go func() { r, e := c.HandleSemanticAction(ctx, action); done <- outcome{r, e} }()
				select {
				case got := <-done:
					if got.err != nil {
						t.Fatal(got.err)
					}
					return got.result
				case <-time.After(2 * time.Second):
					t.Fatal("UI blocked on held recovery")
					return telegramcontroller.SemanticActionResult{}
				}
			}
			initial := call(telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticResume, SessionID: lost.ID()})
			if initial.Card == nil || !initial.Card.Recovery {
				t.Fatalf("held recovery is not a recovery card: %+v", initial.Card)
			}
			projected, err := (telegramruntimecomposition.ControllerFlowAdapter{Controller: c}).HandleMessage(ctx, coordinator.Update{ID: 7000, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "/status"})
			if err != nil || projected.Card == nil || projected.Card.ScreenEligible {
				t.Fatalf("recovery projection allows native Screen: %+v err=%v", projected.Card, err)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("manual recoverer not started")
			}
			// Multiple concurrent taps must share one exact recovery attempt.
			taps := make(chan error, 8)
			for i := 0; i < 8; i++ {
				go func() {
					_, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticResume, SessionID: lost.ID()})
					taps <- err
				}()
			}
			for i := 0; i < 8; i++ {
				select {
				case err := <-taps:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("duplicate recovery tap blocked")
				}
			}
			if attempts.Load() != 1 {
				t.Fatalf("duplicate concurrent recovery attempts=%d", attempts.Load())
			}
			call(telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticPagePrevious, SessionID: lost.ID(), Page: 1})
			before, err := store.LoadCardHistory(ctx, lost.ID())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.HandleSemanticMessage(ctx, coordinator.Update{ID: 7001, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "must remain blocked"}); err != nil {
				t.Fatal(err)
			}
			after, err := store.LoadCardHistory(ctx, lost.ID())
			if err != nil || len(before) != len(after) {
				t.Fatal("input entered history during recovery", err)
			}
			if scenario.switchAway {
				call(telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: other.ID()})
			}
			finish()
			var notice telegramcontroller.Notification
			select {
			case notice = <-notices:
			case <-time.After(2 * time.Second):
				t.Fatal("recovery refresh missing")
			}
			if notice.Kind != telegramcontroller.NotificationPromptStatus || notice.SessionID != lost.ID() {
				t.Fatalf("recovery completion notice=%+v", notice)
			}
			stored, err := store.Load(ctx, lost.ID())
			if err != nil {
				t.Fatal(err)
			}
			want := domain.SessionAwaitingRecovery
			if scenario.persist {
				want = domain.SessionReady
			}
			if stored.Status() != want {
				t.Fatalf("persisted outcome=%s want=%s", stored.Status(), want)
			}
			if !scenario.persist && !strings.Contains(notice.Text, "не подтверждён") {
				t.Fatalf("unproven outcome falsely successful: %q", notice.Text)
			}
			if !scenario.persist {
				if _, err := c.HandleSemanticMessage(ctx, coordinator.Update{ID: 7002, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "still must remain blocked"}); err != nil {
					t.Fatal(err)
				}
				history, err := store.LoadCardHistory(ctx, lost.ID())
				if err != nil || len(history) != len(before) {
					t.Fatal("unknown recovery accepted subsequent input", err)
				}
			}
			current, err := c.ProjectCurrent(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			active, err := store.LoadActiveSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantActive := lost.ID()
			if scenario.switchAway {
				wantActive = other.ID()
			}
			if active != wantActive || current.Card == nil || current.Card.SessionID != wantActive {
				t.Fatalf("completion stole foreground: persisted=%s card=%+v want=%s", active, current.Card, wantActive)
			}
			if scenario.persist {
				exact, err := c.ProjectCurrent(ctx, lost.ID())
				if err != nil || exact.Card == nil || exact.Card.Recovery {
					t.Fatalf("recovered projection=%+v err=%v", exact.Card, err)
				}
				pages := exact.Card.Pages
				if len(pages) != 2 || pages[1].Content != "EXACT_RECOVERED_FINAL" || exact.Card.View.Page != 2 || !exact.Card.View.FollowLatest {
					t.Fatalf("proven final not shown on fresh final page: %+v", exact.Card)
				}
			}
			if err := c.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 1 {
				t.Fatalf("duplicate recovery attempts after completion=%d", attempts.Load())
			}
			if probe.calls.Load() != 0 {
				t.Fatalf("recovery submitted input %d times", probe.calls.Load())
			}
		})
	}
}

func TestRecoveryAwaitingSessionCanBeArchived(t *testing.T) {
	ctx := context.Background()
	store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111", "22222222-2222-4222-9222-222222222222")
	awaiting, err := sessions[0].AwaitRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(ctx, sessions[0], awaiting); err != nil {
		t.Fatal(err)
	}
	closer, err := app.NewSessionCloser(store, archiveStarter{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	c := archiveController(t, store, nil, telegramcontroller.Options{Recovered: []domain.Session{sessions[1]}, UIState: store, SessionCloser: closer})
	defer c.Close(ctx)
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: awaiting.ID()}); err != nil {
		t.Fatal(err)
	}
	result, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: awaiting.ID(), Choice: 1})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(ctx, awaiting.ID())
	if err != nil || stored.Status() != domain.SessionArchived {
		t.Fatalf("recovery archive=%s err=%v", stored.Status(), err)
	}
	if result.Card == nil || result.Card.SessionID != sessions[1].ID() {
		t.Fatalf("archive did not select remaining session: %+v", result)
	}
}
