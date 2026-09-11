package nativeadapter

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/nativetranscript"
	"bria/internal/runtimeprotocol"
)

func correlationAdapter(t *testing.T) (*adapter, *bytes.Buffer) {
	t.Helper()
	output := &bytes.Buffer{}
	input := func(id, text string) *activeInput {
		return &activeInput{request: runtimeprotocol.ParentMessage{RequestID: id, MessageID: id + "-message"}, text: text, sent: time.Now()}
	}
	return &adapter{config: Config{StateDir: filepath.Join(t.TempDir(), "private")}, id: fixtureSession, output: output, receipts: map[string]string{}, active: input("root", "first"), steers: []*activeInput{input("steer", "second")}}, output
}

func TestQueuedSteerWaitsForOwnNativeTurnCompletion(t *testing.T) {
	a, output := correlationAdapter(t)
	if err := a.consumeEvents([]nativetranscript.Event{
		{Kind: nativetranscript.KindUser, TurnID: "t1", Text: "first"},
		{Kind: nativetranscript.KindUser, TurnID: "t2", Text: "second"},
		{Kind: nativetranscript.KindFinal, TurnID: "t1", Text: "first final"},
		{Kind: nativetranscript.KindComplete, TurnID: "t1"},
	}); err != nil {
		t.Fatal(err)
	}
	if a.active == nil || a.receipts["root-message"] != "completed" || a.receipts["steer-message"] != "unknown" {
		t.Fatal("first terminal completed or discarded queued native turn")
	}
	if bytes.Contains(output.Bytes(), []byte(`"type":"completed"`)) {
		t.Fatal("logical turn completed before queued native input")
	}
	output.Reset()
	if err := a.consumeEvents([]nativetranscript.Event{
		{Kind: nativetranscript.KindFinal, TurnID: "foreign", Text: "foreign final"},
		{Kind: nativetranscript.KindComplete, TurnID: "foreign"},
	}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || a.final != "first final" {
		t.Fatal("unrelated turn changed active result")
	}
	if err := a.consumeEvents([]nativetranscript.Event{
		{Kind: nativetranscript.KindFinal, TurnID: "t2", Text: "second final"},
		{Kind: nativetranscript.KindComplete, TurnID: "t2"},
	}); err != nil {
		t.Fatal(err)
	}
	if a.active != nil || a.receipts["steer-message"] != "completed" {
		t.Fatal("queued terminal did not complete logical turn")
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("terminal frame count=%d", len(lines))
	}
	final, err := runtimeprotocol.DecodeAdapterLine(lines[0], runtimeprotocol.Limits{})
	if err != nil || final.Type != runtimeprotocol.TypeFinal || final.RequestID != "steer" || final.Text != "second final" {
		t.Fatal("queued result lost latest applied input correlation")
	}
}

func TestUnacceptedQueuedInputIsNotDroppedAtRootTerminal(t *testing.T) {
	a, _ := correlationAdapter(t)
	if err := a.consumeEvents([]nativetranscript.Event{{Kind: nativetranscript.KindUser, TurnID: "t1", Text: "first"}, {Kind: nativetranscript.KindComplete, TurnID: "t1"}}); err != nil {
		t.Fatal(err)
	}
	if a.active == nil || len(a.steers) != 1 || a.steers[0].accepted {
		t.Fatal("root terminal discarded unaccepted steer")
	}
	if err := a.consumeEvents([]nativetranscript.Event{{Kind: nativetranscript.KindUser, TurnID: "t2", Text: "second"}, {Kind: nativetranscript.KindComplete, TurnID: "t2"}}); err != nil {
		t.Fatal(err)
	}
	if a.active != nil || a.receipts["steer-message"] != "completed" {
		t.Fatal("delayed accepted steer did not finish")
	}
}

func TestInterruptOnlyMarksMatchingTurnFailed(t *testing.T) {
	a, _ := correlationAdapter(t)
	if err := a.consumeEvents([]nativetranscript.Event{{Kind: nativetranscript.KindUser, TurnID: "t1", Text: "first"}, {Kind: nativetranscript.KindUser, TurnID: "t2", Text: "second"}, {Kind: nativetranscript.KindInterrupted, TurnID: "t1"}}); err != nil {
		t.Fatal(err)
	}
	if a.receipts["root-message"] != "failed" || a.receipts["steer-message"] != "unknown" {
		t.Fatal("interrupt claimed fate of different queued turn")
	}
}

func TestAcceptanceWithoutTurnIdentityFailsClosed(t *testing.T) {
	a, output := correlationAdapter(t)
	if err := a.consumeEvents([]nativetranscript.Event{{Kind: nativetranscript.KindUser, Text: "first"}}); err == nil {
		t.Fatal("missing identity accepted")
	}
	if output.Len() != 0 || a.active.accepted {
		t.Fatal("unbound input produced acceptance")
	}
}

func TestSameNativeTurnSteerCompletesWithRoot(t *testing.T) {
	a, _ := correlationAdapter(t)
	if err := a.consumeEvents([]nativetranscript.Event{
		{Kind: nativetranscript.KindUser, TurnID: "t1", Text: "first"},
		{Kind: nativetranscript.KindUser, TurnID: "t1", Text: "second"},
		{Kind: nativetranscript.KindComplete, TurnID: "t1"},
	}); err != nil {
		t.Fatal(err)
	}
	if a.active != nil || a.receipts["root-message"] != "completed" || a.receipts["steer-message"] != "completed" {
		t.Fatal("same-turn steer did not complete with matching root")
	}
}

func TestSameNativeTurnModelEventsFollowExactFIFOInputBoundaries(t *testing.T) {
	a, output := correlationAdapter(t)
	a.steers[0].text = "repeated steer"
	a.steers = append(a.steers, &activeInput{
		request: runtimeprotocol.ParentMessage{RequestID: "steer-2", MessageID: "steer-2-message"},
		text:    "repeated steer",
		sent:    time.Now(),
	})
	if err := a.consumeEvents([]nativetranscript.Event{
		{Kind: nativetranscript.KindUser, TurnID: "same-turn", Text: "first"},
		{Kind: nativetranscript.KindCommentary, TurnID: "same-turn", Text: "before first steer"},
		{Kind: nativetranscript.KindUser, TurnID: "same-turn", Text: "repeated steer"},
		{Kind: nativetranscript.KindCommentary, TurnID: "same-turn", Text: "after first steer"},
		{Kind: nativetranscript.KindUser, TurnID: "same-turn", Text: "repeated steer"},
		{Kind: nativetranscript.KindThinking, TurnID: "same-turn", Text: "after second steer"},
	}); err != nil {
		t.Fatal(err)
	}

	var got []string
	decoder := json.NewDecoder(output)
	for decoder.More() {
		var message runtimeprotocol.AdapterMessage
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		if message.Type == runtimeprotocol.TypeEvent {
			got = append(got, message.RequestID+":"+message.Text)
		}
	}
	want := []string{
		"root:before first steer",
		"steer:after first steer",
		"steer-2:after second steer",
	}
	if len(got) != len(want) {
		t.Fatalf("model event count=%d, want=%d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("model event %d=%q, want %q; all=%v", i, got[i], want[i], got)
		}
	}
}

func TestClaudeUsesExactNativeUserBoundaryWhenAvailable(t *testing.T) {
	a, output := correlationAdapter(t)
	a.config.Provider = domain.ProviderClaude
	a.steers = append(a.steers, &activeInput{
		request: runtimeprotocol.ParentMessage{RequestID: "steer-2", MessageID: "steer-2-message"},
		text:    "third",
		sent:    time.Now(),
	})
	if err := a.consumeEvents([]nativetranscript.Event{
		{Kind: nativetranscript.KindUser, TurnID: "claude-root", Text: "first"},
		{Kind: nativetranscript.KindUser, TurnID: "claude-root", Text: "second"},
		{Kind: nativetranscript.KindUser, TurnID: "claude-root", Text: "third"},
		{Kind: nativetranscript.KindCommentary, TurnID: "foreign", Text: "must not leak"},
		{Kind: nativetranscript.KindCommentary, TurnID: "claude-root", Text: "stream after applied input"},
	}); err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(output)
	var messages []runtimeprotocol.AdapterMessage
	for decoder.More() {
		var message runtimeprotocol.AdapterMessage
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		if message.Type == runtimeprotocol.TypeEvent {
			messages = append(messages, message)
		}
	}
	if len(messages) != 1 {
		t.Fatalf("fallback emitted %d events, want one bound event: %+v", len(messages), messages)
	}
	message := messages[0]
	if message.Type != runtimeprotocol.TypeEvent || message.RequestID != "steer-2" || message.Text != "stream after applied input" {
		t.Fatalf("Claude event=%+v, want latest applied input", message)
	}
}

func TestStructuredQuestionPreservesPendingTurnAndExactCorrelation(t *testing.T) {
	a, output := correlationAdapter(t)
	if err := a.consumeEvents([]nativetranscript.Event{
		{Kind: nativetranscript.KindUser, TurnID: "t1", Text: "first"},
		{Kind: nativetranscript.KindQuestion, TurnID: "foreign", Text: "must not leak"},
		{Kind: nativetranscript.KindQuestion, TurnID: "t1", Text: "Which option?"},
	}); err != nil {
		t.Fatal(err)
	}
	if a.active == nil || a.active.completed || a.receipts["root-message"] != "unknown" {
		t.Fatal("pending question completed active turn")
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("question frames=%d, want accepted+question", len(lines))
	}
	event, err := runtimeprotocol.DecodeAdapterLine(lines[1], runtimeprotocol.Limits{})
	if err != nil || event.Type != runtimeprotocol.TypeEvent || event.Kind != "question" || event.RequestID != "root" || event.Text != "Which option?" {
		t.Fatal("structured question routing lost correlation")
	}
}

func TestNativeEventsPreserveThinkingAndToolMetadata(t *testing.T) {
	a, output := correlationAdapter(t)
	metadata := &runtimeprotocol.EventMetadata{ItemID: "tool-1", Name: "Read", Arguments: `{"path":"README.md"}`, Result: "ok", Status: "completed"}
	if err := a.consumeEvents([]nativetranscript.Event{
		{Kind: nativetranscript.KindUser, TurnID: "t1", Text: "first"},
		{Kind: nativetranscript.KindThinking, TurnID: "t1", Text: "inspect"},
		{Kind: nativetranscript.KindTool, TurnID: "t1", Text: "Read", Metadata: metadata},
	}); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("frames=%d", len(lines))
	}
	thinking, err := runtimeprotocol.DecodeAdapterLine(lines[1], runtimeprotocol.Limits{})
	if err != nil || thinking.Kind != "thinking" || thinking.Text != "inspect" {
		t.Fatalf("thinking=%#v %v", thinking, err)
	}
	tool, err := runtimeprotocol.DecodeAdapterLine(lines[2], runtimeprotocol.Limits{})
	if err != nil || tool.Kind != "tool" || tool.Text != "Read" || tool.EventMetadata == nil ||
		tool.EventMetadata.ItemID != metadata.ItemID || tool.EventMetadata.Arguments != metadata.Arguments ||
		tool.EventMetadata.Result != metadata.Result || tool.EventMetadata.Status != metadata.Status {
		t.Fatalf("tool=%#v %v", tool, err)
	}
}
