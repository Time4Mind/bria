package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/provider/codex"
)

func TestClientRunsPersistentThreadAndReturnsOnlyCompletedFinalAnswer(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1","codexHome":"/redacted","platformFamily":"unix","platformOs":"macos"}}`,
		`{"method":"account/rateLimits/updated","params":{"rateLimits":{}}}`,
		`{"id":2,"result":{"thread":{"id":"thread-1","sessionId":"session-1","ephemeral":false,"path":"/redacted/thread.jsonl","cwd":"/tmp/bria-codex-probe","cliVersion":"0.152.1","status":{"type":"idle"}},"model":"gpt-5.6-sol","approvalPolicy":"untrusted","sandbox":{"type":"readOnly","networkAccess":false}}}`,
		`{"method":"thread/started","params":{"thread":{"id":"thread-1"}}}`,
		`{"method":"warning","params":{"message":"requested approval policy was overridden"}}`,
		`{"id":3,"result":{"turn":{"id":"turn-1","items":[],"itemsView":"notLoaded","status":"inProgress","error":null}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"futureItem","id":47,"futureShape":[true]}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"item-commentary","text":"not the answer","phase":"commentary"}}}`,
		`{"method":"future/notification","params":{"value":7}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"item-final","text":"BRIA_CODEX_PROBE_OK","phase":"final_answer"}}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","items":[{"type":"agentMessage","id":"item-final","text":"BRIA_CODEX_PROBE_OK","phase":"final_answer"}],"itemsView":"summary","status":"completed","error":null}}}`,
	}, "\n") + "\n"

	var output bytes.Buffer
	var notifications []string
	client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
		OnNotification: func(notification codex.Notification) error {
			notifications = append(notifications, notification.Method)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	initialized, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if initialized.UserAgent != "codex-cli/0.152.1" {
		t.Fatalf("Initialize().UserAgent = %q, want codex-cli/0.152.1", initialized.UserAgent)
	}

	thread, err := client.StartThread(context.Background(), codex.ThreadStartRequest{
		Cwd:            "/tmp/bria-codex-probe",
		ApprovalPolicy: "never",
		Sandbox:        "read-only",
	})
	if err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}
	if thread.ThreadID != "thread-1" || thread.SessionID != "thread-1" || thread.ReportedSessionID != "session-1" {
		t.Fatalf("StartThread() ids = (%q, %q, %q), want (thread-1, thread-1, session-1)", thread.ThreadID, thread.SessionID, thread.ReportedSessionID)
	}
	if !thread.HasEffectiveApprovalPolicy || thread.EffectiveApprovalPolicy != "untrusted" || !thread.ApprovalPolicyOverridden {
		t.Fatalf("StartThread() effective approval = %q, overridden = %t", thread.EffectiveApprovalPolicy, thread.ApprovalPolicyOverridden)
	}
	if thread.RequestedApprovalPolicy != "never" || thread.RequestedSandbox != "read-only" {
		t.Fatalf("StartThread() requested policy projection = %#v", thread)
	}
	if !thread.HasEffectiveSandbox || thread.EffectiveSandbox != (codex.Sandbox{Type: "readOnly", NetworkAccess: false}) {
		t.Fatalf("StartThread() effective sandbox = %#v", thread.EffectiveSandbox)
	}
	if thread.SandboxOverridden {
		t.Fatalf("StartThread() reports equivalent sandbox spellings as an override")
	}

	outcome, err := client.StartTurn(context.Background(), codex.TurnStartRequest{
		ThreadID:  "thread-1",
		MessageID: "telegram:42:8",
		Input:     []codex.TextInput{{Text: "Reply exactly."}},
		SandboxPolicy: &codex.SandboxPolicy{
			Type:          "readOnly",
			NetworkAccess: false,
		},
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if outcome.Status != "completed" || outcome.ThreadID != "thread-1" || outcome.TurnID != "turn-1" {
		t.Fatalf("StartTurn() outcome = %#v", outcome)
	}
	if outcome.Final == nil || outcome.Final.ItemID != "item-final" || outcome.Final.Text != "BRIA_CODEX_PROBE_OK" {
		t.Fatalf("StartTurn() final = %#v", outcome.Final)
	}
	wantNotifications := []string{
		"account/rateLimits/updated",
		"thread/started",
		"warning",
		"item/completed",
		"item/completed",
		"future/notification",
		"item/completed",
		"turn/completed",
	}
	if !reflect.DeepEqual(notifications, wantNotifications) {
		t.Fatalf("notification order = %#v, want %#v", notifications, wantNotifications)
	}

	wantOutput := strings.Join([]string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"bria","version":"0.1.0"},"capabilities":{}}}`,
		`{"method":"initialized","params":{}}`,
		`{"id":2,"method":"thread/start","params":{"cwd":"/tmp/bria-codex-probe","ephemeral":false,"approvalPolicy":"never","sandbox":"read-only"}}`,
		`{"id":3,"method":"turn/start","params":{"threadId":"thread-1","input":[{"type":"text","text":"Reply exactly."}],"clientUserMessageId":"telegram:42:8","sandboxPolicy":{"type":"readOnly","networkAccess":false}}}`,
	}, "\n") + "\n"
	if output.String() != wantOutput {
		t.Fatalf("wire output:\n%s\nwant:\n%s", output.String(), wantOutput)
	}
}

func TestClientListsThreadsThroughStateDBOnlyWithOfficialSchema(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1"}}`,
		`{"id":2,"result":{"data":[{"id":"thread-1","sessionId":"session-1","cwd":"/work/project","createdAt":1788336000,"updatedAt":1788336060,"ephemeral":false,"cliVersion":"0.152.1","modelProvider":"openai","preview":"secret prompt is deliberately ignored","projectId":null,"source":"cli","status":{"type":"idle"},"turns":[]}],"nextCursor":"cursor-2","backwardsCursor":"ignored"}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	page, err := client.ListThreads(context.Background(), codex.ThreadListRequest{Limit: 25})
	if err != nil {
		t.Fatalf("ListThreads() error = %v", err)
	}
	want := codex.ThreadListPage{
		Threads: []codex.ThreadSummary{{
			ID: "thread-1", Cwd: "/work/project",
			CreatedAt: time.Unix(1788336000, 0).UTC(),
			UpdatedAt: time.Unix(1788336060, 0).UTC(),
		}},
		NextCursor: "cursor-2",
	}
	if !reflect.DeepEqual(page, want) {
		t.Fatalf("ListThreads() = %#v, want %#v", page, want)
	}
	wantOutput := strings.Join([]string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"bria","version":"0.1.0"},"capabilities":{}}}`,
		`{"method":"initialized","params":{}}`,
		`{"id":2,"method":"thread/list","params":{"limit":25,"sortKey":"updated_at","sortDirection":"desc","useStateDbOnly":true}}`,
	}, "\n") + "\n"
	if output.String() != wantOutput {
		t.Fatalf("wire output:\n%s\nwant:\n%s", output.String(), wantOutput)
	}
}

func TestStartTurnEncodesOfficialLocalImageInputSeparatelyFromText(t *testing.T) {
	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1"}}`,
		`{"id":2,"result":{"turn":{"id":"turn-image"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-image","item":{"type":"agentMessage","id":"final-image","text":"seen","phase":"final_answer"}}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-image","status":"completed","error":null}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = client.StartTurn(context.Background(), codex.TurnStartRequest{
		ThreadID: "thread-1", MessageID: "telegram:photo:1",
		Input:       []codex.TextInput{{Text: "inspect"}},
		LocalImages: []codex.LocalImageInput{{Path: "/private/tmp/provider-photo.png"}},
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	want := strings.Join([]string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"bria","version":"0.1.0"},"capabilities":{}}}`,
		`{"method":"initialized","params":{}}`,
		`{"id":2,"method":"turn/start","params":{"threadId":"thread-1","input":[{"type":"text","text":"inspect"},{"type":"localImage","path":"/private/tmp/provider-photo.png"}],"clientUserMessageId":"telegram:photo:1"}}`,
	}, "\n") + "\n"
	if output.String() != want {
		t.Fatalf("wire output:\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestClientListsTurnsWithExactClientUserMessageIDs(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1"}}`,
		`{"id":2,"result":{"data":[{"id":"turn-2","items":[{"type":"userMessage","id":"user-2","clientId":"telegram:42:8","content":[{"type":"text","text":"secret prompt ignored"}]},{"type":"agentMessage","id":"agent-2","text":"secret answer ignored","phase":"final_answer"}],"itemsView":"full","status":"completed","error":null},{"id":"turn-1","items":[{"type":"userMessage","id":"user-1","clientId":null,"content":[{"type":"text","text":"unmanaged"}]}],"itemsView":"full","status":"interrupted","error":null}],"nextCursor":"older","backwardsCursor":"newer"}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	page, err := client.ListThreadTurns(context.Background(), codex.ThreadTurnsListRequest{
		ThreadID: "thread-1", Limit: 25,
	})
	if err != nil {
		t.Fatalf("ListThreadTurns() error = %v", err)
	}
	want := codex.ThreadTurnsListPage{
		Turns: []codex.ThreadTurnSummary{
			{ID: "turn-2", MessageIDs: []string{"telegram:42:8"}, Status: codex.ThreadTurnCompleted},
			{ID: "turn-1", Status: codex.ThreadTurnInterrupted},
		},
		NextCursor: "older",
	}
	if !reflect.DeepEqual(page, want) {
		t.Fatalf("ListThreadTurns() = %#v, want %#v", page, want)
	}
	wantOutput := strings.Join([]string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"bria","version":"0.1.0"},"capabilities":{}}}`,
		`{"method":"initialized","params":{}}`,
		`{"id":2,"method":"thread/turns/list","params":{"threadId":"thread-1","limit":25,"itemsView":"full","sortDirection":"desc"}}`,
	}, "\n") + "\n"
	if output.String() != wantOutput {
		t.Fatalf("wire output:\n%s\nwant:\n%s", output.String(), wantOutput)
	}
}

func TestClientRejectsAmbiguousClientIDsWithoutReturningPartialTurnPage(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1"}}`,
		`{"id":2,"result":{"data":[{"id":"turn-1","items":[{"type":"userMessage","id":"user-1","clientId":"m1","content":[]},{"type":"userMessage","id":"user-2","clientId":"m1","content":[]}],"itemsView":"full","status":"completed","error":null}],"nextCursor":null}}`,
	}, "\n") + "\n"
	client, err := codex.NewClient(strings.NewReader(transcript), io.Discard, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if _, err := client.ListThreadTurns(context.Background(), codex.ThreadTurnsListRequest{ThreadID: "thread-1", Limit: 25}); !errors.Is(err, codex.ErrInvalidResponse) {
		t.Fatalf("ListThreadTurns() error = %v, want %v", err, codex.ErrInvalidResponse)
	}
}

func TestClientRejectsCorruptThreadListWithoutReturningPartialPage(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1"}}`,
		`{"id":2,"result":{"data":[{"id":"thread-1","cwd":"/work","createdAt":20,"updatedAt":10,"ephemeral":false}]}}`,
	}, "\n") + "\n"
	client, err := codex.NewClient(strings.NewReader(transcript), io.Discard, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if _, err := client.ListThreads(context.Background(), codex.ThreadListRequest{Limit: 25}); !errors.Is(err, codex.ErrInvalidResponse) {
		t.Fatalf("ListThreads() error = %v, want ErrInvalidResponse", err)
	}
}

func TestThreadStartCanUseProviderDefaultPolicyAndTurnAcceptanceIsAuthoritative(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1"}}`,
		`{"id":2,"result":{"thread":{"id":"thread-1","sessionId":"session-1","ephemeral":false,"cwd":"/tmp/work"},"approvalPolicy":"untrusted","sandbox":{"type":"readOnly","networkAccess":false}}}`,
		`{"id":3,"result":{"turn":{"id":"turn-1","items":[],"status":"inProgress","error":null}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"final-1","text":"done","phase":"final_answer"}}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","items":[],"status":"completed","error":null}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	var accepted []codex.TurnAccepted
	client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	thread, err := client.StartThread(context.Background(), codex.ThreadStartRequest{Cwd: "/tmp/work"})
	if err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}
	if thread.EffectiveApprovalPolicy != "untrusted" || thread.EffectiveSandbox.Type != "readOnly" {
		t.Fatalf("StartThread() effective policy = %#v", thread)
	}
	if thread.ApprovalPolicyOverridden || thread.SandboxOverridden {
		t.Fatalf("StartThread() must not report an override when no policy was requested: %#v", thread)
	}
	if _, err := client.StartTurn(context.Background(), codex.TurnStartRequest{
		ThreadID: "thread-1",
		Input:    []codex.TextInput{{Text: "hello"}},
		OnAccepted: func(event codex.TurnAccepted) error {
			accepted = append(accepted, event)
			return nil
		},
	}); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if want := []codex.TurnAccepted{{ThreadID: "thread-1", TurnID: "turn-1"}}; !reflect.DeepEqual(accepted, want) {
		t.Fatalf("accepted callbacks = %#v, want %#v", accepted, want)
	}
	wantThreadStart := `{"id":2,"method":"thread/start","params":{"cwd":"/tmp/work","ephemeral":false}}` + "\n"
	if !strings.Contains(output.String(), wantThreadStart) {
		t.Fatalf("wire output = %q, want provider-default thread/start %q", output.String(), wantThreadStart)
	}
}

func TestThreadStartAcceptsDocumentedMinimalResponseAndUsesThreadIDBinding(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/current"}}`,
		`{"id":2,"result":{"thread":{"id":"thread-minimal"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	thread, err := client.StartThread(context.Background(), codex.ThreadStartRequest{Cwd: "/tmp/work"})
	if err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}
	if thread.ThreadID != "thread-minimal" || thread.SessionID != "thread-minimal" || thread.Cwd != "/tmp/work" {
		t.Fatalf("StartThread() result = %#v", thread)
	}
	if thread.EffectiveApprovalPolicy != "" || thread.EffectiveSandbox != (codex.Sandbox{}) ||
		thread.ApprovalPolicyOverridden || thread.SandboxOverridden {
		t.Fatalf("StartThread() invented absent effective policy fields: %#v", thread)
	}
}

func TestThreadStartCanResumePersistedThreadIDWithDocumentedShape(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/current"}}`,
		`{"id":2,"result":{"thread":{"id":"thread-persisted","name":"existing","ephemeral":false}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	thread, err := client.StartThread(context.Background(), codex.ThreadStartRequest{
		ResumeThreadID: "thread-persisted",
	})
	if err != nil {
		t.Fatalf("StartThread(resume) error = %v", err)
	}
	if thread.ThreadID != "thread-persisted" || thread.SessionID != "thread-persisted" {
		t.Fatalf("StartThread(resume) result = %#v", thread)
	}
	want := `{"id":2,"method":"thread/resume","params":{"threadId":"thread-persisted"}}` + "\n"
	if !strings.HasSuffix(output.String(), want) {
		t.Fatalf("wire output = %q, want suffix %q", output.String(), want)
	}
}

func TestTurnCompletionIsAuthoritativeAndDoesNotInventFinalFromSummary(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1"}}`,
		`{"id":2,"result":{"thread":{"id":"thread-1","sessionId":"session-1","ephemeral":false,"cwd":"/tmp/work"},"approvalPolicy":"untrusted","sandbox":{"type":"readOnly","networkAccess":false}}}`,
		`{"id":3,"result":{"turn":{"id":"turn-1","items":[{"type":"agentMessage","id":"not-authoritative","text":"wrong","phase":"final_answer"}],"status":"inProgress","error":null}}}`,
		`{"method":"item/completed","params":{"threadId":"other-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"other-final","text":"wrong","phase":"final_answer"}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"discarded-final","text":"must not publish","phase":"final_answer"}}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","items":[{"type":"agentMessage","id":"summary-only","text":"wrong","phase":"final_answer"}],"status":"failed","error":{"code":"model_error","message":"safe structured detail"}}}}`,
	}, "\n") + "\n"

	client, err := codex.NewClient(strings.NewReader(transcript), io.Discard, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if _, err := client.StartThread(context.Background(), codex.ThreadStartRequest{
		Cwd: "/tmp/work", ApprovalPolicy: "never", Sandbox: "read-only",
	}); err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}
	outcome, err := client.StartTurn(context.Background(), codex.TurnStartRequest{
		ThreadID: "thread-1",
		Input:    []codex.TextInput{{Text: "hello"}},
	})
	var terminal *codex.TurnTerminalError
	if !errors.As(err, &terminal) || terminal.Status != "failed" || !terminal.HasServerError {
		t.Fatalf("StartTurn() error = %#v, want failed TurnTerminalError with server error", err)
	}
	if outcome.Status != "failed" {
		t.Fatalf("StartTurn().Status = %q, want failed", outcome.Status)
	}
	if outcome.Final != nil {
		t.Fatalf("StartTurn().Final = %#v, want nil without item/completed final_answer", outcome.Final)
	}
	var turnError map[string]any
	if err := json.Unmarshal(outcome.Error, &turnError); err != nil {
		t.Fatalf("StartTurn().Error = %s: %v", outcome.Error, err)
	}
	if turnError["code"] != "model_error" {
		t.Fatalf("StartTurn().Error code = %#v", turnError["code"])
	}
}

func TestMalformedAndOversizedMessagesFailWithoutEchoingInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		maxBytes  int
		wantError error
	}{
		{
			name:      "malformed",
			input:     `not-json secret-token /private/path` + "\n",
			maxBytes:  1024,
			wantError: codex.ErrMalformedMessage,
		},
		{
			name:      "oversized",
			input:     strings.Repeat("secret-token/private/path", 8) + "\n",
			maxBytes:  32,
			wantError: codex.ErrMessageTooLarge,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client, err := codex.NewClient(strings.NewReader(test.input), io.Discard, codex.Options{
				ClientInfo:      codex.ClientInfo{Name: "bria", Version: "0.1.0"},
				MaxMessageBytes: test.maxBytes,
			})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			_, err = client.Initialize(context.Background())
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Initialize() error = %v, want %v", err, test.wantError)
			}
			for _, secret := range []string{"secret-token", "/private/path"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Initialize() error leaks %q: %v", secret, err)
				}
			}
		})
	}
}

func TestRemoteAndTransportErrorsDoNotEchoServerOrPathDetails(t *testing.T) {
	t.Parallel()

	t.Run("remote", func(t *testing.T) {
		t.Parallel()
		client, err := codex.NewClient(
			strings.NewReader(`{"id":1,"error":{"code":-32000,"message":"secret-token /private/path"}}`+"\n"),
			io.Discard,
			codex.Options{ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"}},
		)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		_, err = client.Initialize(context.Background())
		var remote *codex.RemoteError
		if !errors.As(err, &remote) || remote.Code != -32000 {
			t.Fatalf("Initialize() error = %#v, want RemoteError code -32000", err)
		}
		assertNoSensitiveDetails(t, err)
	})

	t.Run("reader", func(t *testing.T) {
		t.Parallel()
		client, err := codex.NewClient(
			errorReader{err: errors.New("secret-token /private/path")},
			io.Discard,
			codex.Options{ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"}},
		)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		_, err = client.Initialize(context.Background())
		assertNoSensitiveDetails(t, err)
	})

	t.Run("writer", func(t *testing.T) {
		t.Parallel()
		client, err := codex.NewClient(
			strings.NewReader(""),
			errorWriter{err: errors.New("secret-token /private/path")},
			codex.Options{ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"}},
		)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		_, err = client.Initialize(context.Background())
		assertNoSensitiveDetails(t, err)
	})
}

func TestResponseCorrelationPreservesInterleavedUnknownNotification(t *testing.T) {
	t.Parallel()

	wantParams := json.RawMessage(`{"opaque":[1,{"future":true}]}`)
	transcript := strings.Join([]string{
		`{"id":99,"result":{"future":true}}`,
		`{"method":"future/notification","params":{"opaque":[1,{"future":true}]}}`,
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1"}}`,
	}, "\n") + "\n"
	var got []codex.Notification
	client, err := codex.NewClient(strings.NewReader(transcript), io.Discard, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
		OnNotification: func(notification codex.Notification) error {
			got = append(got, notification)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if len(got) != 1 || got[0].Method != "future/notification" || !bytes.Equal(got[0].Params, wantParams) {
		t.Fatalf("notifications = %#v, want preserved future notification", got)
	}
}

func TestInterruptRequestUsesOfficialShape(t *testing.T) {
	clientInput, serverOutput := io.Pipe()
	serverInput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer serverInput.Close()
	defer clientOutput.Close()
	defer serverOutput.Close()

	interruptSeen := make(chan struct{})
	allowInterruptResponse := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(serverInput)
		encoder := json.NewEncoder(serverOutput)
		var request struct {
			ID     codex.RequestID `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		for _, response := range []any{
			map[string]any{"id": 1, "result": map[string]any{"userAgent": "codex-cli/current"}},
			map[string]any{"id": 2, "result": map[string]any{"thread": map[string]any{"id": "thread-1"}}},
			map[string]any{"id": 3, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}},
		} {
			if err := decoder.Decode(&request); err != nil {
				serverDone <- err
				return
			}
			if err := encoder.Encode(response); err != nil {
				serverDone <- err
				return
			}
			if request.Method == "initialize" {
				var initialized map[string]any
				if err := decoder.Decode(&initialized); err != nil {
					serverDone <- err
					return
				}
			}
		}
		if err := decoder.Decode(&request); err != nil {
			serverDone <- err
			return
		}
		var interruptParams map[string]string
		if json.Unmarshal(request.Params, &interruptParams) != nil || request.ID != 4 || request.Method != "turn/interrupt" ||
			interruptParams["threadId"] != "thread-1" || interruptParams["turnId"] != "turn-1" {
			serverDone <- errors.New("unexpected turn/interrupt request")
			return
		}
		close(interruptSeen)
		<-allowInterruptResponse
		if err := encoder.Encode(map[string]any{"id": 4, "result": map[string]any{}}); err != nil {
			serverDone <- err
			return
		}
		serverDone <- encoder.Encode(map[string]any{
			"method": "turn/completed",
			"params": map[string]any{"threadId": "thread-1", "turn": map[string]any{
				"id": "turn-1", "status": "interrupted", "error": nil,
			}},
		})
	}()

	accepted := make(chan struct{})
	client, err := codex.NewClient(clientInput, clientOutput, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"}, InputCloser: clientInput,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if _, err := client.StartThread(context.Background(), codex.ThreadStartRequest{
		Cwd: "/tmp/work", ApprovalPolicy: "never", Sandbox: "read-only",
	}); err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}
	turnDone := make(chan struct {
		outcome codex.TurnOutcome
		err     error
	}, 1)
	go func() {
		outcome, err := client.StartTurn(context.Background(), codex.TurnStartRequest{
			ThreadID: "thread-1", Input: []codex.TextInput{{Text: "hello"}},
			OnAccepted: func(codex.TurnAccepted) error { close(accepted); return nil },
		})
		turnDone <- struct {
			outcome codex.TurnOutcome
			err     error
		}{outcome: outcome, err: err}
	}()
	<-accepted
	if _, err := client.RequestInterrupt(context.Background(), "other-thread", "turn-1"); !errors.Is(err, codex.ErrActiveTurnMismatch) {
		t.Fatalf("mismatched RequestInterrupt() error = %v", err)
	}
	interruptDone := make(chan struct {
		id  codex.RequestID
		err error
	}, 1)
	go func() {
		id, err := client.RequestInterrupt(context.Background(), "thread-1", "turn-1")
		interruptDone <- struct {
			id  codex.RequestID
			err error
		}{id: id, err: err}
	}()
	<-interruptSeen
	select {
	case result := <-interruptDone:
		t.Fatalf("RequestInterrupt() returned before correlated response: %#v", result)
	case <-time.After(25 * time.Millisecond):
	}
	close(allowInterruptResponse)
	interruptResult := <-interruptDone
	if interruptResult.err != nil || interruptResult.id != 4 {
		t.Fatalf("RequestInterrupt() = (%d, %v), want (4, nil)", interruptResult.id, interruptResult.err)
	}
	turnResult := <-turnDone
	outcome, err := turnResult.outcome, turnResult.err
	var terminal *codex.TurnTerminalError
	if !errors.As(err, &terminal) || terminal.Status != "interrupted" || terminal.HasServerError {
		t.Fatalf("StartTurn() = (%#v, %#v), want interrupted terminal outcome", outcome, err)
	}
	if _, err := client.RequestInterrupt(context.Background(), "thread-1", "turn-1"); !errors.Is(err, codex.ErrNoActiveTurn) {
		t.Fatalf("post-completion RequestInterrupt() error = %v, want ErrNoActiveTurn", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("fake server error = %v", err)
	}
}

func TestInterruptBeforeInitializationIsRejectedWithoutWireWrite(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	client, err := codex.NewClient(strings.NewReader(""), &output, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.RequestInterrupt(context.Background(), "thread-1", "turn-1"); !errors.Is(err, codex.ErrNotInitialized) {
		t.Fatalf("RequestInterrupt() error = %v, want ErrNotInitialized", err)
	}
	if output.Len() != 0 {
		t.Fatalf("RequestInterrupt() wrote before handshake: %q", output.String())
	}
}

func TestUnsupportedServerRequestsInterruptActiveTurnBeforeFailing(t *testing.T) {
	t.Parallel()

	for _, method := range []string{
		"item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/tool/requestUserInput",
	} {
		method := method
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			transcript := strings.Join([]string{
				`{"id":1,"result":{"userAgent":"codex-cli/current"}}`,
				`{"id":2,"result":{"thread":{"id":"thread-1"}}}`,
				`{"id":3,"result":{"turn":{"id":"turn-1"}}}`,
				`{"id":901,"method":"` + method + `","params":{"threadId":"thread-1","turnId":"turn-1","reason":"secret-token /private/path"}}`,
			}, "\n") + "\n"
			var output bytes.Buffer
			client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{
				ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
			})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			if _, err := client.Initialize(context.Background()); err != nil {
				t.Fatalf("Initialize() error = %v", err)
			}
			if _, err := client.StartThread(context.Background(), codex.ThreadStartRequest{Cwd: "/tmp/work"}); err != nil {
				t.Fatalf("StartThread() error = %v", err)
			}
			_, err = client.StartTurn(context.Background(), codex.TurnStartRequest{
				ThreadID: "thread-1", Input: []codex.TextInput{{Text: "hello"}},
			})
			if !errors.Is(err, codex.ErrUnsupportedRequest) {
				t.Fatalf("StartTurn() error = %v, want ErrUnsupportedRequest", err)
			}
			assertNoSensitiveDetails(t, err)
			want := `{"id":4,"method":"turn/interrupt","params":{"threadId":"thread-1","turnId":"turn-1"}}` + "\n"
			if !strings.HasSuffix(output.String(), want) {
				t.Fatalf("wire output = %q, want interrupt suffix %q", output.String(), want)
			}
		})
	}
}

func TestClientHandlesTypedUserInputRequestAndWritesCorrelatedResponse(t *testing.T) {
	t.Parallel()

	transcript := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/current"}}`,
		`{"id":2,"result":{"thread":{"id":"thread-1"}}}`,
		`{"id":3,"result":{"turn":{"id":"turn-1"}}}`,
		`{"id":"ask-1","method":"item/tool/requestUserInput","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","questions":[{"id":"q1","header":"Choice","question":"Pick one","options":[{"label":"First","description":"Use first"}],"isOther":true,"isSecret":false}],"isBlocking":true}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"final-1","text":"done","phase":"final_answer"}}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","error":null}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	var got codex.ServerRequest
	client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{
		ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
		OnServerRequest: func(_ context.Context, request codex.ServerRequest) (codex.ServerResponse, error) {
			got = request
			return codex.ServerResponse{Answers: map[string][]string{"q1": {"First"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if _, err := client.StartThread(context.Background(), codex.ThreadStartRequest{Cwd: "/tmp/work"}); err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}
	outcome, err := client.StartTurn(context.Background(), codex.TurnStartRequest{
		ThreadID: "thread-1", Input: []codex.TextInput{{Text: "ask"}},
	})
	if err != nil || outcome.Final == nil || outcome.Final.Text != "done" {
		t.Fatalf("StartTurn() = (%#v, %v), want completed final", outcome, err)
	}
	want := codex.ServerRequest{
		InteractionID: "string:ask-1", Kind: codex.ServerRequestQuestion,
		ThreadID: "thread-1", TurnID: "turn-1",
		Question: &codex.QuestionRequest{
			ItemID: "item-1", IsBlocking: true,
			Questions: []codex.UserInputQuestion{{
				ID: "q1", Header: "Choice", Question: "Pick one", IsOther: true,
				Options: []codex.UserInputOption{{Label: "First", Description: "Use first"}},
			}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("typed server request = %#v, want %#v", got, want)
	}
	wantResponse := `{"id":"ask-1","result":{"answers":{"q1":{"answers":["First"]}}}}` + "\n"
	if !strings.Contains(output.String(), wantResponse) {
		t.Fatalf("wire output = %q, want correlated response %q", output.String(), wantResponse)
	}
	wantInitialize := `{"id":1,"method":"initialize","params":{"clientInfo":{"name":"bria","version":"0.1.0"},"capabilities":{"experimentalApi":true}}}` + "\n"
	if !strings.HasPrefix(output.String(), wantInitialize) {
		t.Fatalf("wire output = %q, want explicit interaction capability prefix %q", output.String(), wantInitialize)
	}
}

func TestClientHandlesTypedPermissionRequestsAndPreservesNumericCorrelation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		params     string
		decision   codex.ApprovalDecision
		wantKind   codex.ServerRequestKind
		want       codex.PermissionRequest
		wantResult string
	}{
		{
			name: "command", method: "item/commandExecution/requestApproval",
			params:   `{"threadId":"thread-1","turnId":"turn-1","itemId":"cmd-1","approvalId":"approval-1","startedAtMs":17,"reason":"needed","command":"go test ./...","cwd":"/tmp/work"}`,
			decision: codex.ApprovalCancel, wantKind: codex.ServerRequestCommandPermission,
			want:       codex.PermissionRequest{ItemID: "cmd-1", ApprovalID: "approval-1", StartedAtMS: 17, Reason: "needed", Command: "go test ./...", Cwd: "/tmp/work"},
			wantResult: `{"id":901,"result":{"decision":"cancel"}}` + "\n",
		},
		{
			name: "file", method: "item/fileChange/requestApproval",
			params:   `{"threadId":"thread-1","turnId":"turn-1","itemId":"file-1","startedAtMs":18,"reason":"write","grantRoot":"/tmp/work"}`,
			decision: codex.ApprovalAccept, wantKind: codex.ServerRequestFilePermission,
			want:       codex.PermissionRequest{ItemID: "file-1", StartedAtMS: 18, Reason: "write", GrantRoot: "/tmp/work"},
			wantResult: `{"id":901,"result":{"decision":"accept"}}` + "\n",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transcript := strings.Join([]string{
				`{"id":1,"result":{"userAgent":"codex-cli/current"}}`,
				`{"id":2,"result":{"thread":{"id":"thread-1"}}}`,
				`{"id":3,"result":{"turn":{"id":"turn-1"}}}`,
				`{"id":901,"method":"` + test.method + `","params":` + test.params + `}`,
				`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","error":null}}}`,
			}, "\n") + "\n"
			var output bytes.Buffer
			var got codex.ServerRequest
			client, err := codex.NewClient(strings.NewReader(transcript), &output, codex.Options{
				ClientInfo: codex.ClientInfo{Name: "bria", Version: "0.1.0"},
				OnServerRequest: func(_ context.Context, request codex.ServerRequest) (codex.ServerResponse, error) {
					got = request
					return codex.ServerResponse{Decision: test.decision}, nil
				},
			})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			if _, err := client.Initialize(context.Background()); err != nil {
				t.Fatalf("Initialize() error = %v", err)
			}
			if _, err := client.StartThread(context.Background(), codex.ThreadStartRequest{Cwd: "/tmp/work"}); err != nil {
				t.Fatalf("StartThread() error = %v", err)
			}
			if _, err := client.StartTurn(context.Background(), codex.TurnStartRequest{
				ThreadID: "thread-1", Input: []codex.TextInput{{Text: "act"}},
			}); err != nil {
				t.Fatalf("StartTurn() error = %v", err)
			}
			if got.InteractionID != "number:901" || got.Kind != test.wantKind || got.ThreadID != "thread-1" || got.TurnID != "turn-1" ||
				got.Question != nil || got.Permission == nil || *got.Permission != test.want {
				t.Fatalf("typed permission = %#v, want kind %q permission %#v", got, test.wantKind, test.want)
			}
			if !strings.Contains(output.String(), test.wantResult) {
				t.Fatalf("wire output = %q, want correlated response %q", output.String(), test.wantResult)
			}
		})
	}
}

func TestCanceledContextClosesInjectedInputAndUnblocksRead(t *testing.T) {
	t.Parallel()

	reader := newBlockingReadCloser(strings.Join([]string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.152.1"}}`,
		`{"id":2,"result":{"thread":{"id":"thread-1","sessionId":"session-1","ephemeral":false,"cwd":"/tmp/work"},"approvalPolicy":"untrusted","sandbox":{"type":"readOnly","networkAccess":false}}}`,
		`{"id":3,"result":{"turn":{"id":"turn-1","items":[],"status":"inProgress","error":null}}}`,
	}, "\n") + "\n")
	client, err := codex.NewClient(struct{ io.Reader }{Reader: reader}, io.Discard, codex.Options{
		ClientInfo:  codex.ClientInfo{Name: "bria", Version: "0.1.0"},
		InputCloser: reader,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if _, err := client.StartThread(context.Background(), codex.ThreadStartRequest{
		Cwd: "/tmp/work", ApprovalPolicy: "never", Sandbox: "read-only",
	}); err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(ctx, codex.TurnStartRequest{
			ThreadID: "thread-1",
			Input:    []codex.TextInput{{Text: "hello"}},
		})
		result <- err
	}()
	select {
	case <-reader.blocked:
	case <-time.After(time.Second):
		t.Fatal("StartTurn() did not reach blocking read")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StartTurn() error = %v, want context.Canceled", err)
		}
		assertNoSensitiveDetails(t, err)
	case <-time.After(time.Second):
		_ = reader.Close()
		t.Fatal("StartTurn() remained blocked after cancellation")
	}
	if got := reader.closeCount(); got != 1 {
		t.Fatalf("input Close() calls = %d, want 1", got)
	}
}

func assertNoSensitiveDetails(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want failure")
	}
	for _, secret := range []string{"secret-token", "/private/path"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}
}

type errorReader struct {
	err error
}

func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

type errorWriter struct {
	err error
}

func (writer errorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

type blockingReadCloser struct {
	data      *strings.Reader
	blocked   chan struct{}
	closed    chan struct{}
	blockOnce sync.Once
	closeOnce sync.Once
	mu        sync.Mutex
	closes    int
}

func newBlockingReadCloser(script string) *blockingReadCloser {
	return &blockingReadCloser{
		data:    strings.NewReader(script),
		blocked: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (reader *blockingReadCloser) Read(buffer []byte) (int, error) {
	if reader.data.Len() > 0 {
		return reader.data.Read(buffer)
	}
	reader.blockOnce.Do(func() { close(reader.blocked) })
	<-reader.closed
	return 0, errors.New("secret-token /private/path")
}

func (reader *blockingReadCloser) Close() error {
	reader.mu.Lock()
	reader.closes++
	reader.mu.Unlock()
	reader.closeOnce.Do(func() { close(reader.closed) })
	return errors.New("secret-token /private/path")
}

func (reader *blockingReadCloser) closeCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.closes
}
