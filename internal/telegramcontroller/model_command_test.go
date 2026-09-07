package telegramcontroller_test

import (
	"context"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

type nativeFake struct {
	requests       []sessionruntime.NativeRequest
	err            error
	noninteractive bool
}

func (n *nativeFake) NativeControl(_ context.Context, _ domain.SessionID, r sessionruntime.NativeRequest) (sessionruntime.NativeSnapshot, error) {
	n.requests = append(n.requests, r)
	return sessionruntime.NativeSnapshot{Text: "CLI model: actual-model\n❯ actual-model ✓\n  other-model", Hash: strings.Repeat("a", 64), Model: "actual-model", Interactive: !n.noninteractive}, n.err
}

func TestNativeControlsExistOnlyWhileCLIIsInteractive(t *testing.T) {
	ctx := context.Background()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-model", 1)
	native := &nativeFake{}
	c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, Native: native})
	defer c.Close(ctx)
	if _, err := c.HandleSemanticMessage(ctx, message(960, "/model")); err != nil {
		t.Fatal(err)
	}
	back, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: ready.ID()})
	if err != nil || back.Card == nil || back.Surface != nil || len(native.requests) != 1 {
		t.Fatalf("Back must show regular card without CLI key: %+v %v", back, err)
	}
	if _, err := c.HandleSemanticMessage(ctx, message(961, "/model")); err != nil {
		t.Fatal(err)
	}
	native.noninteractive = true
	done, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticNativeKey, SessionID: ready.ID(), Choice: 5})
	if err != nil || done.Card == nil || done.Surface != nil {
		t.Fatalf("selection completed but native keys remain: %+v %v", done, err)
	}
	count := len(native.requests)
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticNativeKey, SessionID: ready.ID(), Choice: 2}); err != nil || len(native.requests) != count {
		t.Fatalf("key sent after CLI stopped selecting: %v %+v", err, native.requests)
	}
	r, err := c.HandleSemanticMessage(ctx, message(962, "/help"))
	if err != nil || r.Surface == nil || r.Surface.NativeSessionID != "" {
		t.Fatalf("noninteractive command opened overlay: %+v %v", r, err)
	}
	for _, row := range r.Surface.Rows {
		for _, button := range row {
			if button.Action == telegramcontroller.SemanticNativeKey {
				t.Fatal("noninteractive output exposes terminal keys")
			}
		}
	}
}

func TestNativeMenuRestoresAfterSwitchingSessionOrOpeningMenu(t *testing.T) {
	ctx := context.Background()
	first := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/one", "provider-one", 1)
	second := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderCodex, "/two", "provider-two", 1)
	native := &nativeFake{}
	c := newController(t, nil, newLockedSessions(first, second), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{first, second}, Native: native})
	defer c.Close(ctx)
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: first.ID()}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.HandleSemanticMessage(ctx, message(970, "/model")); err != nil {
		t.Fatal(err)
	}
	r, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: second.ID()})
	if err != nil || r.Card == nil || r.Card.SessionID != second.ID() {
		t.Fatalf("switch to other session: %+v %v", r, err)
	}
	r, err = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: first.ID()})
	if err != nil || r.Surface == nil || r.Surface.NativeSessionID != first.ID() {
		t.Fatalf("switch back lost CLI picker: %+v %v", r, err)
	}
	if _, err := c.HandleSemanticMessage(ctx, message(971, "/menu")); err != nil {
		t.Fatal(err)
	}
	r, err = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuSessions})
	if err != nil || r.Surface == nil || r.Surface.NativeSessionID != first.ID() {
		t.Fatalf("menu reopening lost CLI picker: %+v %v", r, err)
	}
	if len(native.requests) != 1 {
		t.Fatalf("navigation emitted terminal input: %+v", native.requests)
	}
	native.noninteractive, native.err = true, sessionruntime.ErrNativeStale
	r, err = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticNativeKey, SessionID: first.ID(), Choice: 2})
	if err != nil || r.Card == nil || r.Surface != nil {
		t.Fatalf("stale closed menu must restore regular card: %+v %v", r, err)
	}
}

func TestModelCommandNeverBecomesPromptWithoutNativeTerminal(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-model", 1)
	c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}})
	defer c.Close(context.Background())
	r, err := c.HandleSemanticMessage(context.Background(), message(919, "/model"))
	if err != nil || r.Surface == nil || !strings.Contains(r.Surface.Text, "недоступен") {
		t.Fatalf("native unavailable = %#v %v", r, err)
	}
}

func TestNativeCommandsRenderCLIScreenAndKeysWithoutModelCatalog(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-model", 1)
	native := &nativeFake{}
	c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, Native: native})
	defer c.Close(context.Background())
	for i, command := range []string{"/model", "/effort", "/some-new-cli-command value", "plain interactive answer"} {
		r, err := c.HandleSemanticMessage(context.Background(), message(int64(920+i), command))
		if err != nil || r.Surface == nil || !strings.Contains(r.Surface.Text, "actual-model ✓") || len(r.Surface.Rows) != 4 {
			t.Fatalf("native screen = %#v %v", r, err)
		}
		if len(native.requests) != i+1 || native.requests[i].Command != command {
			t.Fatalf("command changed or duplicate: %#v", native.requests)
		}
	}
	r, err := c.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticNativeKey, SessionID: ready.ID(), Choice: 2})
	if err != nil || r.Surface == nil || native.requests[4].Key != "down" || native.requests[4].ExpectedHash != strings.Repeat("a", 64) {
		t.Fatalf("native key = %#v %v %#v", r, err, native.requests)
	}
	_, err = c.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticNativeKey, SessionID: "other", Choice: 5})
	if err != nil || len(native.requests) != 5 {
		t.Fatalf("wrong-session key dispatched: %v %#v", err, native.requests)
	}
	_, err = c.HandleSemanticMessage(context.Background(), message(930, "/menu"))
	if err != nil || len(native.requests) != 5 {
		t.Fatalf("Bria menu dispatched to CLI: %v %#v", err, native.requests)
	}
	r, err = c.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: ready.ID()})
	if err != nil || r.Surface == nil || !strings.Contains(r.Surface.Text, "actual-model ✓") {
		t.Fatalf("reopening session lost interactive CLI: %#v %v", r, err)
	}
	native.err = sessionruntime.ErrNativeStale
	r, err = c.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticNativeKey, SessionID: ready.ID(), Choice: 5})
	if err != nil || r.Surface == nil || len(native.requests) != 6 {
		t.Fatalf("stale screen must refresh without retry: %#v %v %#v", r, err, native.requests)
	}
	unauthorized := message(931, "/model")
	unauthorized.ActorID++
	_, err = c.HandleSemanticMessage(context.Background(), unauthorized)
	if err != nil || len(native.requests) != 6 {
		t.Fatalf("unauthorized native command: %v %#v", err, native.requests)
	}
}

func TestNativeOverlayPreservesFinalHistoryAndReturnsWithoutProjectionReopeningIt(t *testing.T) {
	ctx := context.Background()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-model", 1)
	native := &nativeFake{}
	state := &projectionUIState{history: map[domain.SessionID][]string{ready.ID(): {"original prompt"}}}
	c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, Native: native, UIState: state})
	defer c.Close(ctx)
	if _, err := c.HandleSemanticMessage(ctx, message(940, "/model")); err != nil {
		t.Fatal(err)
	}
	if err := state.AppendCardHistory(ctx, ready.ID(), "durable final arrived"); err != nil {
		t.Fatal(err)
	}
	card, active, err := c.ProjectCompletion(ctx, ready.ID())
	if err != nil || active || !strings.Contains(card.Pages[len(card.Pages)-1].Content, "durable final arrived") {
		t.Fatalf("final must use notification path and preserve full history: %+v active=%t err=%v", card, active, err)
	}
	r, err := c.ProjectCurrent(ctx, ready.ID())
	if err != nil || r.Surface == nil || r.Surface.NativeSessionID != ready.ID() {
		t.Fatalf("overlay lost: %+v %v", r, err)
	}
	_, err = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticNativeKey, SessionID: ready.ID(), Choice: 2})
	if err != nil || len(native.requests) != 2 || native.requests[1].ExpectedHash == "" {
		t.Fatalf("completion broke terminal keys: %v %+v", err, native.requests)
	}
	if _, err := c.HandleSemanticMessage(ctx, message(941, "/menu")); err != nil {
		t.Fatal(err)
	}
	r, err = c.ProjectCurrent(ctx, ready.ID())
	if err != nil || r.Card == nil || !strings.Contains(r.Card.Pages[len(r.Card.Pages)-1].Content, "durable final arrived") {
		t.Fatalf("stored final unavailable after closing overlay: %+v %v", r, err)
	}
	_, active, err = c.ProjectCompletion(ctx, ready.ID())
	if err != nil || !active {
		t.Fatalf("read-only projection reopened overlay: active=%t %v", active, err)
	}
}

func TestNativeUnavailableDisplaysSnapshotReasonButNeverRawError(t *testing.T) {
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-model", 1)
	native := &nativeFake{err: sessionruntime.ErrNativeUnavailable}
	c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, Native: native})
	defer c.Close(context.Background())
	r, err := c.HandleSemanticMessage(context.Background(), message(950, "/model"))
	if err != nil || r.Surface == nil || !strings.Contains(r.Surface.Text, "Команда CLI недоступна") || !strings.Contains(r.Surface.Text, "actual-model") || strings.Contains(r.Surface.Text, sessionruntime.ErrNativeUnavailable.Error()) {
		t.Fatalf("native error reason = %+v %v", r, err)
	}
}

type cachedNativeFake struct {
	nativeFake
	model string
}

func (n *cachedNativeFake) NativeModel(domain.SessionID) (string, bool) {
	return n.model, n.model != ""
}

func TestInitialCardHydratesActualNativeModelWithoutOpeningOverlay(t *testing.T) {
	ctx := context.Background()
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-model", 1)
	native := &cachedNativeFake{}
	c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, Native: native})
	defer c.Close(ctx)
	r, err := c.ProjectCurrent(ctx, ready.ID())
	if err != nil || r.Card == nil || strings.Contains(r.Card.Header, "actual-model") {
		t.Fatalf("unknown runtime model must not label card: %+v %v", r, err)
	}
	native.model = "actual-model"
	r, err = c.ProjectCurrent(ctx, ready.ID())
	if err != nil || r.Card == nil || r.Surface != nil || !strings.Contains(r.Card.Header, "actual-model") {
		t.Fatalf("actual initial model missing or overlay opened: %+v %v", r, err)
	}
	if len(native.requests) != 0 {
		t.Fatalf("card rendering sent terminal IO: %+v", native.requests)
	}
	native.model = "new-actual-model"
	if r, err := c.ProjectCurrent(ctx, ready.ID()); err != nil || r.Card == nil || !strings.Contains(r.Card.Header, "new-actual-model") {
		t.Fatalf("runtime model changed but header did not: %+v %v", r, err)
	}
	native.model = ""
	if r, err := c.ProjectCurrent(ctx, ready.ID()); err != nil || r.Card == nil || strings.Contains(r.Card.Header, "actual-model") {
		t.Fatalf("retired generation retained stale model: %+v %v", r, err)
	}
	_, active, err := c.ProjectCompletion(ctx, ready.ID())
	if err != nil || !active {
		t.Fatalf("initial snapshot opened native overlay: active=%t %v", active, err)
	}
}
