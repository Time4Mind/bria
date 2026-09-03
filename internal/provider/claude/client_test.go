package claude_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/provider/claude"
)

const probePrompt = "Reply with exactly BRIA_CLAUDE_PROBE_OK. Do not inspect or modify files."

func TestSendUserEmitsAcceptedClaudeEnvelope(t *testing.T) {
	var output bytes.Buffer
	client := newClient(t, strings.NewReader(""), &output, claude.Options{})

	if err := client.SendUser(probePrompt); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}
	want := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Reply with exactly BRIA_CLAUDE_PROBE_OK. Do not inspect or modify files."}]}}` + "\n"
	if got := output.String(); got != want {
		t.Fatalf("wire input = %q, want %q", got, want)
	}
}

func TestSendUserWithIDCarriesExactDurableMessageIdentity(t *testing.T) {
	const messageID = "telegram-update:77"
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"same text"}]},"session_id":"one","uuid":"` + messageID + `","isReplay":true}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	client := newClient(t, strings.NewReader(transcript), &output, claude.Options{})

	if err := client.SendUserWithID("same text", messageID); err != nil {
		t.Fatalf("SendUserWithID() error = %v", err)
	}
	want := `{"type":"user","uuid":"telegram-update:77","message":{"role":"user","content":[{"type":"text","text":"same text"}]}}` + "\n"
	if got := output.String(); got != want {
		t.Fatalf("wire input = %q, want %q", got, want)
	}
	_ = nextEvent(t, client)
	replay := nextEvent(t, client)
	if replay.UserReplay == nil || replay.UserReplay.MessageID != messageID {
		t.Fatalf("replay = %#v, want exact message id %q", replay, messageID)
	}
}

func TestSendUserWithIDRejectsSameTextReplayWithDifferentIdentity(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"same text"}]},"session_id":"one","uuid":"telegram-update:older","isReplay":true}`,
	}, "\n") + "\n"
	client := newClient(t, strings.NewReader(transcript), io.Discard, claude.Options{})

	if err := client.SendUserWithID("same text", "telegram-update:newer"); err != nil {
		t.Fatalf("SendUserWithID() error = %v", err)
	}
	_ = nextEvent(t, client)
	if _, err := client.NextEvent(); !errors.Is(err, claude.ErrReplayMismatch) {
		t.Fatalf("NextEvent() error = %v, want %v", err, claude.ErrReplayMismatch)
	}
}

func TestTypedPermissionControlRequestAndCorrelatedResponse(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"one","isReplay":true}`,
		`{"type":"control_request","request_id":"permission-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"go test ./...","description":"run tests","secret":"must-not-escape"},"tool_use_id":"tool-1","title":"Run command"}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	client := newClient(t, strings.NewReader(transcript), &output, claude.Options{})
	if err := client.SendUser("hello"); err != nil {
		t.Fatal(err)
	}
	_ = nextEvent(t, client)
	_ = nextEvent(t, client)
	event := nextEvent(t, client)
	if event.Kind != claude.EventPermissionRequest || event.Permission == nil {
		t.Fatalf("event = %#v", event)
	}
	want := &claude.PermissionRequest{RequestID: "permission-1", ToolName: "Bash", ToolUseID: "tool-1", Command: "go test ./...", Title: "Run command", Description: "run tests"}
	if !reflect.DeepEqual(event.Permission, want) {
		t.Fatalf("permission = %#v, want %#v", event.Permission, want)
	}
	if strings.Contains(output.String(), "must-not-escape") {
		t.Fatal("unselected provider input escaped to parent output")
	}
	if err := client.RespondPermission(context.Background(), "permission-1", claude.PermissionAllow); err != nil {
		t.Fatalf("RespondPermission() error = %v", err)
	}
	wantWire := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n" +
		`{"type":"control_response","response":{"subtype":"success","request_id":"permission-1","response":{"behavior":"allow"}}}` + "\n"
	if output.String() != wantWire {
		t.Fatalf("wire = %q, want %q", output.String(), wantWire)
	}
	if err := client.RespondPermission(context.Background(), "permission-1", claude.PermissionAllow); !errors.Is(err, claude.ErrMalformedEvent) {
		t.Fatalf("duplicate response error = %v", err)
	}
}

func TestObservedAuthenticationFailureTranscript(t *testing.T) {
	transcript := readFixture(t, "testdata/claude_2_1_181_auth_failure.ndjson")
	var output bytes.Buffer
	client := newClient(t, bytes.NewReader(transcript), &output, claude.Options{})
	if err := client.SendUser(probePrompt); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}

	init := nextEvent(t, client)
	if init.Kind != claude.EventInit || init.SessionID != "fceb8b8f-2763-45b8-907d-bf4b0e20ff0c" {
		t.Fatalf("init = %#v", init)
	}
	if !client.ProtocolInitialized() {
		t.Fatal("ProtocolInitialized() = false after system/init")
	}
	if got, ok := client.SessionID(); !ok || got != init.SessionID {
		t.Fatalf("SessionID() = %q, %v", got, ok)
	}
	if init.Init == nil || init.Init.CWD != "/private/tmp/bria-claude-probe" || init.Init.ClaudeCodeVersion != "2.1.181" {
		t.Fatalf("init detail = %#v", init.Init)
	}

	replay := nextEvent(t, client)
	if replay.Kind != claude.EventUserReplay || replay.UserReplay == nil || replay.UserReplay.Text != probePrompt {
		t.Fatalf("replay = %#v", replay)
	}

	assistant := nextEvent(t, client)
	if assistant.Kind != claude.EventAssistant || assistant.Assistant == nil {
		t.Fatalf("assistant = %#v", assistant)
	}
	if assistant.Assistant.FailureCode != claude.FailureAuthentication {
		t.Fatalf("assistant failure = %q", assistant.Assistant.FailureCode)
	}
	if assistant.Assistant.Text != "" {
		t.Fatalf("authentication failure leaked message text %q", assistant.Assistant.Text)
	}

	terminal := nextEvent(t, client)
	if terminal.Kind != claude.EventResult || terminal.Result == nil {
		t.Fatalf("terminal = %#v", terminal)
	}
	if terminal.Result.FailureCode != claude.FailureAuthentication {
		t.Fatalf("terminal failure = %q", terminal.Result.FailureCode)
	}
	if terminal.Result.TerminalSuccess(nil) {
		t.Fatal("subtype=success with is_error=true reported terminal success")
	}
	if !client.ProtocolInitialized() {
		t.Fatal("authentication failure reset protocol initialization")
	}
}

func TestSuccessfulTranscriptAndProcessOutcomeAreSeparate(t *testing.T) {
	transcript := readFixture(t, "testdata/claude_2_1_181_success_shape.ndjson")
	var output bytes.Buffer
	client := newClient(t, bytes.NewReader(transcript), &output, claude.Options{})
	if err := client.SendUser("hello"); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}

	for range 3 {
		_ = nextEvent(t, client)
	}
	terminal := nextEvent(t, client)
	if terminal.Result == nil || !terminal.Result.TerminalSuccess(nil) {
		t.Fatalf("terminal = %#v, want success", terminal)
	}
	if terminal.Result.TerminalSuccess(errors.New("process exited with status 1")) {
		t.Fatal("process error was ignored")
	}

	if err := client.SendUser("second turn"); err != nil {
		t.Fatalf("SendUser(second turn) error = %v", err)
	}
	if !strings.Contains(output.String(), `"text":"second turn"`) {
		t.Fatalf("second turn was not written: %q", output.String())
	}
}

func TestRejectsSessionMismatchAndKnownEventsOutOfOrder(t *testing.T) {
	for _, test := range []struct {
		name       string
		transcript string
		wantError  error
	}{
		{
			name: "mismatch",
			transcript: `{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}` + "\n" +
				`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"two","isReplay":true}` + "\n",
			wantError: claude.ErrSessionMismatch,
		},
		{
			name: "out of order",
			transcript: `{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}` + "\n" +
				`{"type":"result","subtype":"success","is_error":false,"result":"answer","session_id":"one","terminal_reason":"completed"}` + "\n",
			wantError: claude.ErrEventOutOfOrder,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, strings.NewReader(test.transcript), io.Discard, claude.Options{})
			if err := client.SendUser("hello"); err != nil {
				t.Fatalf("SendUser() error = %v", err)
			}
			_ = nextEvent(t, client)
			_, err := client.NextEvent()
			if !errors.Is(err, test.wantError) {
				t.Fatalf("NextEvent() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestRejectsReplayThatDoesNotMatchPendingInput(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"stale input"}]},"session_id":"one","isReplay":true}`,
	}, "\n") + "\n"
	client := newClient(t, strings.NewReader(transcript), io.Discard, claude.Options{})
	if err := client.SendUser("current input"); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}
	_ = nextEvent(t, client)
	if _, err := client.NextEvent(); !errors.Is(err, claude.ErrReplayMismatch) {
		t.Fatalf("NextEvent() error = %v, want %v", err, claude.ErrReplayMismatch)
	}
}

func TestToolLoopAcceptsInternalUserAndMultipleAssistantEvents(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"one","isReplay":true}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"/private/sensitive"}}]},"session_id":"one"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]},"session_id":"one","isReplay":false}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"answer"}]},"session_id":"one"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"answer","session_id":"one","terminal_reason":"completed"}`,
	}, "\n") + "\n"
	client := newClient(t, strings.NewReader(transcript), io.Discard, claude.Options{})
	if err := client.SendUser("hello"); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}
	wants := []claude.EventKind{
		claude.EventInit,
		claude.EventUserReplay,
		claude.EventAssistant,
		claude.EventInternalUser,
		claude.EventAssistant,
		claude.EventResult,
	}
	var terminal claude.Event
	var firstAssistant claude.Event
	for _, want := range wants {
		terminal = nextEvent(t, client)
		if terminal.Kind != want {
			t.Fatalf("event kind = %v, want %v", terminal.Kind, want)
		}
		if want == claude.EventAssistant && firstAssistant.Assistant == nil {
			firstAssistant = terminal
		}
	}
	if firstAssistant.Assistant == nil || firstAssistant.Assistant.Text != "checking" ||
		!reflect.DeepEqual(firstAssistant.Assistant.Tools, []string{"Read"}) {
		t.Fatalf("first assistant = %#v", firstAssistant)
	}
	if terminal.Result == nil || terminal.Result.Text != "answer" || !terminal.Result.TerminalSuccess(nil) {
		t.Fatalf("terminal = %#v", terminal)
	}
}

func TestReplayRequiresExactSingleTextBlock(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"},{"type":"tool_result","content":"ignored"}]},"session_id":"one","isReplay":true}`,
	}, "\n") + "\n"
	client := newClient(t, strings.NewReader(transcript), io.Discard, claude.Options{})
	if err := client.SendUser("hello"); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}
	_ = nextEvent(t, client)
	if _, err := client.NextEvent(); !errors.Is(err, claude.ErrMalformedEvent) {
		t.Fatalf("NextEvent() error = %v, want %v", err, claude.ErrMalformedEvent)
	}
}

func TestOversizedUnterminatedEventReturnsWithoutWaitingForNewline(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	client := newClient(t, reader, io.Discard, claude.Options{MaxEventBytes: 32})
	done := make(chan error, 1)
	go func() {
		_, err := client.NextEvent()
		done <- err
	}()
	go func() { _, _ = io.WriteString(writer, strings.Repeat("x", 4097)) }()
	select {
	case err := <-done:
		if !errors.Is(err, claude.ErrEventTooLarge) {
			t.Fatalf("NextEvent() error = %v, want %v", err, claude.ErrEventTooLarge)
		}
	case <-time.After(time.Second):
		t.Fatal("NextEvent waited for newline after exceeding limit")
	}
}

func TestTransportErrorsDoNotLeakUnderlyingDetails(t *testing.T) {
	const secret = "secret=/Users/owner/private/token"
	readerClient := newClient(t, errorReader{err: errors.New(secret)}, io.Discard, claude.Options{})
	_, err := readerClient.NextEvent()
	assertSanitizedTransportError(t, err, secret)

	writerClient := newClient(t, strings.NewReader(""), errorWriter{err: errors.New(secret)}, claude.Options{})
	err = writerClient.SendUser("hello")
	assertSanitizedTransportError(t, err, secret)
}

func TestRejectsInvalidUTF8OnInputAndProviderOutput(t *testing.T) {
	client := newClient(t, strings.NewReader(""), io.Discard, claude.Options{})
	if err := client.SendUser(string([]byte{0xff})); !errors.Is(err, claude.ErrMalformedEvent) {
		t.Fatalf("SendUser() error = %v, want %v", err, claude.ErrMalformedEvent)
	}
	client = newClient(t, bytes.NewReader([]byte{'{', 0xff, '}', '\n'}), io.Discard, claude.Options{})
	if _, err := client.NextEvent(); !errors.Is(err, claude.ErrMalformedEvent) {
		t.Fatalf("NextEvent() error = %v, want %v", err, claude.ErrMalformedEvent)
	}
}

func TestNextEventContextClosesSilentReaderAndReturns(t *testing.T) {
	reader := newBlockingReadCloser()
	client := newClient(t, reader, io.Discard, claude.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := client.NextEventContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NextEventContext() error = %v, want deadline exceeded", err)
	}
	select {
	case <-reader.readReturned:
	case <-time.After(time.Second):
		t.Fatal("reader remained blocked after cancellation")
	}
	if !reader.wasClosed() {
		t.Fatal("reader was not closed on cancellation")
	}
}

func TestCancellationUnblocksConcurrentInputAndOutput(t *testing.T) {
	reader := newBlockingReadCloser()
	writer := newBlockingWriteCloser()
	client := newClient(t, reader, writer, claude.Options{})

	sendDone := make(chan error, 1)
	go func() { sendDone <- client.SendUser("hello") }()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("SendUser did not reach writer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	nextDone := make(chan error, 1)
	go func() {
		_, err := client.NextEventContext(ctx)
		nextDone <- err
	}()

	select {
	case err := <-nextDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("NextEventContext() error = %v, want deadline exceeded", err)
		}
	case <-time.After(300 * time.Millisecond):
		_ = reader.Close()
		_ = writer.Close()
		<-nextDone
		t.Fatal("cancellation deadlocked behind a blocked writer")
	}
	if err := <-sendDone; !errors.Is(err, claude.ErrTransport) {
		t.Fatalf("SendUser() error = %v, want %v", err, claude.ErrTransport)
	}
}

func TestUnknownEventsAndFieldsAreTolerated(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"future","session_id":"","new_field":{"nested":true}}`,
		`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181","future":42}`,
		`{"type":"future_after_init","session_id":"one","payload":"ignored"}`,
	}, "\n") + "\n"
	client := newClient(t, strings.NewReader(transcript), io.Discard, claude.Options{})
	if err := client.SendUser("hello"); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}
	first := nextEvent(t, client)
	if first.Kind != claude.EventUnknown || first.UnknownType != "future" {
		t.Fatalf("unknown event = %#v", first)
	}
	if got := nextEvent(t, client); got.Kind != claude.EventInit {
		t.Fatalf("init = %#v", got)
	}
	if got := nextEvent(t, client); got.Kind != claude.EventUnknown || got.UnknownType != "future_after_init" {
		t.Fatalf("unknown event after init = %#v", got)
	}
}

func TestUnknownContentBlockIsIgnoredWithoutParsingItsPayload(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"one","isReplay":true}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"future","text":{"possibly":"sensitive"}},{"type":"text","text":"answer"}]},"session_id":"one"}`,
	}, "\n") + "\n"
	client := newClient(t, strings.NewReader(transcript), io.Discard, claude.Options{})
	if err := client.SendUser("hello"); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}
	_ = nextEvent(t, client)
	_ = nextEvent(t, client)
	assistant := nextEvent(t, client)
	if assistant.Assistant == nil || assistant.Assistant.Text != "answer" {
		t.Fatalf("assistant = %#v", assistant)
	}
}

func TestMalformedKnownAssistantContentIsRejected(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"one","isReplay":true}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text"}]},"session_id":"one"}`,
	}, "\n") + "\n"
	client := newClient(t, strings.NewReader(transcript), io.Discard, claude.Options{})
	if err := client.SendUser("hello"); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}
	_ = nextEvent(t, client)
	_ = nextEvent(t, client)
	if _, err := client.NextEvent(); !errors.Is(err, claude.ErrMalformedEvent) {
		t.Fatalf("NextEvent() error = %v, want %v", err, claude.ErrMalformedEvent)
	}
}

func TestNullTextInKnownContentIsRejected(t *testing.T) {
	for _, test := range []struct {
		name       string
		transcript string
	}{
		{
			name: "replay",
			transcript: strings.Join([]string{
				`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"text","text":null}]},"session_id":"one","isReplay":true}`,
			}, "\n") + "\n",
		},
		{
			name: "assistant",
			transcript: strings.Join([]string{
				`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"one","isReplay":true}`,
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":null}]},"session_id":"one"}`,
			}, "\n") + "\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, strings.NewReader(test.transcript), io.Discard, claude.Options{})
			if err := client.SendUser("hello"); err != nil {
				t.Fatalf("SendUser() error = %v", err)
			}
			_ = nextEvent(t, client)
			if test.name == "assistant" {
				_ = nextEvent(t, client)
			}
			if _, err := client.NextEvent(); !errors.Is(err, claude.ErrMalformedEvent) {
				t.Fatalf("NextEvent() error = %v, want %v", err, claude.ErrMalformedEvent)
			}
		})
	}
}

func TestEventLimitCountsPayloadNotCRLFDelimiter(t *testing.T) {
	payload := `{"type":"future"}`
	client := newClient(
		t,
		strings.NewReader(payload+"\r\n"),
		io.Discard,
		claude.Options{MaxEventBytes: len(payload)},
	)
	if got := nextEvent(t, client); got.Kind != claude.EventUnknown {
		t.Fatalf("event = %#v", got)
	}

	client = newClient(
		t,
		strings.NewReader(payload+" \r\n"),
		io.Discard,
		claude.Options{MaxEventBytes: len(payload)},
	)
	if _, err := client.NextEvent(); !errors.Is(err, claude.ErrEventTooLarge) {
		t.Fatalf("NextEvent() error = %v, want %v", err, claude.ErrEventTooLarge)
	}
}

func TestBoundsEventsAndRejectsMalformedKnownFields(t *testing.T) {
	for _, test := range []struct {
		name       string
		transcript string
		options    claude.Options
		wantError  error
	}{
		{name: "oversized", transcript: strings.Repeat("x", 33) + "\n", options: claude.Options{MaxEventBytes: 32}, wantError: claude.ErrEventTooLarge},
		{name: "invalid json", transcript: "{\n", wantError: claude.ErrMalformedEvent},
		{name: "init missing session", transcript: `{"type":"system","subtype":"init","cwd":"/tmp","claude_code_version":"2.1.181"}` + "\n", wantError: claude.ErrMalformedEvent},
		{name: "replay missing flag", transcript: `{"type":"user","session_id":"one","message":{"role":"user","content":[]}}` + "\n", wantError: claude.ErrMalformedEvent},
		{name: "result missing is_error", transcript: `{"type":"result","session_id":"one","terminal_reason":"completed"}` + "\n", wantError: claude.ErrMalformedEvent},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, strings.NewReader(test.transcript), io.Discard, test.options)
			if strings.Contains(test.name, "replay") || strings.Contains(test.name, "result") {
				// These events are malformed regardless of phase; exercise their required fields directly.
				client = clientAfterInit(t, test.transcript)
			}
			_, err := client.NextEvent()
			if !errors.Is(err, test.wantError) {
				t.Fatalf("NextEvent() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func clientAfterInit(t *testing.T, tail string) *claude.Client {
	t.Helper()
	transcript := `{"type":"system","subtype":"init","cwd":"/tmp","session_id":"one","claude_code_version":"2.1.181"}` + "\n" + tail
	client := newClient(t, strings.NewReader(transcript), io.Discard, claude.Options{})
	if err := client.SendUser("hello"); err != nil {
		t.Fatalf("SendUser() error = %v", err)
	}
	_ = nextEvent(t, client)
	return client
}

func newClient(t *testing.T, reader io.Reader, writer io.Writer, options claude.Options) *claude.Client {
	t.Helper()
	client, err := claude.NewClient(reader, writer, options)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func nextEvent(t *testing.T, client *claude.Client) claude.Event {
	t.Helper()
	event, err := client.NextEvent()
	if err != nil {
		t.Fatalf("NextEvent() error = %v", err)
	}
	return event
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return contents
}

func assertSanitizedTransportError(t *testing.T, err error, secret string) {
	t.Helper()
	if !errors.Is(err, claude.ErrTransport) {
		t.Fatalf("error = %v, want %v", err, claude.ErrTransport)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error leaked underlying detail: %v", err)
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }

type blockingReadCloser struct {
	closed       chan struct{}
	readReturned chan struct{}
	once         sync.Once
}

type blockingWriteCloser struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (writer *blockingWriteCloser) Write([]byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	<-writer.closed
	return 0, errors.New("closed writer detail")
}

func (writer *blockingWriteCloser) Close() error {
	select {
	case <-writer.closed:
	default:
		close(writer.closed)
	}
	return nil
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{
		closed:       make(chan struct{}),
		readReturned: make(chan struct{}),
	}
}

func (reader *blockingReadCloser) Read([]byte) (int, error) {
	<-reader.closed
	reader.once.Do(func() { close(reader.readReturned) })
	return 0, errors.New("closed reader detail")
}

func (reader *blockingReadCloser) Close() error {
	select {
	case <-reader.closed:
	default:
		close(reader.closed)
	}
	return nil
}

func (reader *blockingReadCloser) wasClosed() bool {
	select {
	case <-reader.closed:
		return true
	default:
		return false
	}
}
