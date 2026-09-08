package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/nativetranscript"
	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
)

type retentionSubmitter struct{ events []sessionruntime.TurnEvent }

func (s retentionSubmitter) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("streaming callbacks required")
}

func (s retentionSubmitter) SubmitWithCallbacks(_ context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	for _, event := range s.events {
		if err := callbacks.OnEvent(event); err != nil {
			return sessionruntime.TurnResult{}, err
		}
	}
	return sessionruntime.TurnResult{Final: "RETENTION_FINAL", TerminalStatus: sessionruntime.StatusCompleted}, nil
}

type retentionHTTP func(*http.Request) (*http.Response, error)

func (f retentionHTTP) Do(request *http.Request) (*http.Response, error) { return f(request) }

// This crosses native parsing, the real bounded protocol, controller callbacks,
// on-disk UI history, reopened settings/state and the production Rich sender.
func TestA25ToolRetentionNativeToReopenedRichWire(t *testing.T) {
	for _, sample := range []struct{ name, glyph string }{{"emoji", "🙂"}, {"escaped_control", "\x01"}} {
		for _, count := range []int{4000, 4001} {
			t.Run(fmt.Sprintf("%s_%d", sample.name, count), func(t *testing.T) {
				retentionEndToEnd(t, sample.glyph, count)
			})
		}
	}
}

func retentionEndToEnd(t *testing.T, glyph string, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	const nativeID = "01900000-0000-7000-8000-000000000001"
	const sessionID = domain.SessionID("11111111-1111-4111-9111-111111111111")
	callID := strings.Repeat("c", 1000) // Exercises compact storage with bounded metadata.
	content := []map[string]string{{"type": "text", "text": strings.Repeat(glyph, count)}}
	contentJSON, err := json.Marshal(content)
	if err != nil || len(contentJSON) <= 8192 {
		t.Fatal("fixture must exceed the old native metadata cap")
	}
	var source strings.Builder
	for _, record := range []any{
		map[string]any{"type": "session_meta", "payload": map[string]string{"id": nativeID, "cwd": "/synthetic"}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call", "call_id": callID, "name": "exec", "arguments": ""}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": callID, "output": content}},
	} {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		source.Write(encoded)
		source.WriteByte('\n')
	}
	nativeRoot := filepath.Join(root, "native")
	if err := os.Mkdir(nativeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeRoot, "rollout-"+nativeID+".jsonl"), []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := nativetranscript.Open(ctx, nativetranscript.Options{Provider: "codex", Root: nativeRoot, SessionID: nativeID, Workdir: "/synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	nativeEvents, err := reader.Poll(ctx)
	if err != nil || len(nativeEvents) != 2 {
		t.Fatalf("native events=%d err=%v", len(nativeEvents), err)
	}
	var events []sessionruntime.TurnEvent
	for _, event := range nativeEvents {
		if event.Kind != nativetranscript.KindTool || event.Metadata == nil {
			t.Fatal("missing typed native metadata")
		}
		wire, err := runtimeprotocol.EncodeAdapterLine(runtimeprotocol.AdapterMessage{Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeEvent, RequestID: "retention-request", Kind: "tool", Text: event.Text, EventMetadata: event.Metadata}, runtimeprotocol.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := runtimeprotocol.DecodeAdapterLine(wire, runtimeprotocol.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, sessionruntime.TurnEvent{Kind: sessionruntime.EventTool, Text: decoded.Text, Metadata: decoded.EventMetadata})
	}
	if next, err := reader.Poll(ctx); err != nil || len(next) != 0 {
		t.Fatal("native cursor replayed tool events")
	}
	statePath := filepath.Join(root, "state.json")
	store, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSession(sessionID, "retention-intent", "local", domain.ProviderCodex, "/synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: nativeID, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompareAndSwap(ctx, starting, ready); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(root, "settings.json")
	prefsStore, err := settings.OpenFileStore(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	prefs := settingscomposition.Preferences{Store: prefsStore}
	for range 2 {
		if err := prefs.CycleTechnicalOutputLines(ctx); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan telegramcontroller.Notification, 1)
	c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, retentionSubmitter{events}, archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error {
		if n.Kind == telegramcontroller.NotificationFinal || n.Kind == telegramcontroller.NotificationError {
			done <- n
		}
		return nil
	}), telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: store, Settings: prefs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	if _, err = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: sessionID}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.HandleSemanticMessage(ctx, coordinator.Update{ID: 9201, Kind: coordinator.UpdateMessage, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "synthetic retention request"}); err != nil {
		t.Fatal(err)
	}
	select {
	case notification := <-done:
		if notification.Kind != telegramcontroller.NotificationFinal {
			t.Fatal("controller turn failed")
		}
	case <-ctx.Done():
		t.Fatal("controller completion timeout")
	}
	if err = c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(statePath); err != nil || len(data) == 0 {
		t.Fatal("missing physical session state")
	}
	reopened, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := reopened.LoadCardTranscript(ctx, sessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	tools, compact := 0, false
	for _, block := range blocks {
		if block.Kind != "tool" {
			continue
		}
		tools++
		var envelope struct {
			Encoding  string `json:"encoding"`
			Truncated bool   `json:"truncated"`
		}
		if len(block.Text) > 16384 || json.Unmarshal([]byte(block.Text), &envelope) != nil {
			t.Fatal("invalid physical history item")
		}
		if envelope.Encoding == "rune21-v1" {
			compact = true
			if envelope.Truncated != (count > 4000) {
				t.Fatal("truncation provenance lost on disk")
			}
		}
	}
	if tools != 2 || !compact {
		t.Fatalf("stored tools=%d compact=%t", tools, compact)
	}
	prefsStore, err = settings.OpenFileStore(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	prefs = settingscomposition.Preferences{Store: prefsStore}
	if snapshot, err := prefs.Snapshot(ctx); err != nil || snapshot.TechnicalOutputLines != 40 {
		t.Fatal("maximum setting did not survive reopen")
	}
	c2 := archiveController(t, reopened, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: reopened, Settings: prefs})
	defer c2.Close(context.Background())
	projection, err := c2.ProjectCurrent(ctx, sessionID)
	if err != nil || projection.Card == nil {
		t.Fatalf("reopened projection: %v", err)
	}
	var bodies strings.Builder
	wireCalls := 0
	client, err := telegram.NewClient("123:synthetic-retention", retentionHTTP(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/sendRichMessage") {
			return nil, errors.New("unexpected text transport")
		}
		var payload struct {
			Rich *telegram.InputRichMessage `json:"rich_message"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		if payload.Rich == nil {
			return nil, errors.New("missing rich wire body")
		}
		wireCalls++
		text := payload.Rich.Markdown
		for {
			_, body, found := strings.Cut(text, "</summary>\n\n")
			if !found {
				break
			}
			body, text, found = strings.Cut(body, "\n\n</details>")
			if !found {
				return nil, errors.New("unbalanced wire spoiler")
			}
			bodies.WriteString(html.UnescapeString(body))
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":55,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`))}, nil
	}), telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close(context.Background())
	for index, page := range projection.Card.Pages {
		if len(page.Content) > 3000 || !utf8.ValidString(page.Content) {
			t.Fatal("unbounded page")
		}
		if _, err := sender.SendStatus(ctx, fmt.Sprintf("retention-%d", index), coordinator.Status{ConversationID: 42, Text: page.Content}); err != nil {
			t.Fatal(err)
		}
	}
	want := strings.TrimSuffix(strings.Repeat(strings.Repeat(glyph, 100)+"\n", 40), "\n")
	if count > 4000 {
		want += "\n… (truncated)"
	}
	if bodies.String() != want {
		t.Fatalf("native->wire retained %d runes, want %d", utf8.RuneCountInString(bodies.String()), utf8.RuneCountInString(want))
	}
	if wireCalls != len(projection.Card.Pages) || wireCalls < 2 {
		t.Fatal("did not verify every continuation page")
	}
}
