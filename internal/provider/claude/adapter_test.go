package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testWorkdir   = "/private/tmp/bria-claude-adapter"
	testSessionID = "00000000-0000-4000-8000-000000000000"
)

func TestBuildCommandSpecForcesSafeStreamProtocol(t *testing.T) {
	spec, err := BuildCommandSpec(
		mustTestExecutable(t),
		nil,
		testWorkdir,
		bytes.NewReader(make([]byte, 16)),
	)
	if err != nil {
		t.Fatalf("BuildCommandSpec() error = %v", err)
	}
	if spec.SessionID != testSessionID {
		t.Fatalf("SessionID = %q, want %q", spec.SessionID, testSessionID)
	}
	wantArgs := []string{
		"--bare",
		"--print", "--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--replay-user-messages",
		"--permission-prompt-tool", "stdio",
		"--session-id", testSessionID,
	}
	if strings.Join(spec.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("Args = %#v, want %#v", spec.Args, wantArgs)
	}
}

func TestBuildResumeCommandSpecUsesExactExistingSessionWithoutRandomFallback(t *testing.T) {
	spec, err := BuildResumeCommandSpec(
		mustTestExecutable(t),
		nil,
		testWorkdir,
		testSessionID,
	)
	if err != nil {
		t.Fatalf("BuildResumeCommandSpec() error = %v", err)
	}
	if spec.SessionID != testSessionID {
		t.Fatalf("SessionID = %q, want %q", spec.SessionID, testSessionID)
	}
	wantArgs := []string{
		"--bare",
		"--print", "--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--replay-user-messages",
		"--permission-prompt-tool", "stdio",
		"--resume", testSessionID,
	}
	if strings.Join(spec.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("Args = %#v, want %#v", spec.Args, wantArgs)
	}

	if _, err := BuildResumeCommandSpec(mustTestExecutable(t), nil, testWorkdir, ""); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("missing resume session error = %v, want %v", err, ErrInvalidCommand)
	}
	if _, err := BuildResumeCommandSpec(mustTestExecutable(t), nil, testWorkdir, "NOT-A-UUID"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("invalid resume session error = %v, want %v", err, ErrInvalidCommand)
	}
}

func TestBuildCommandSpecRejectsCallerSessionSelectionAndUnsafeFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--dangerously-skip-permissions"},
		{"--allow-dangerously-skip-permissions"},
		{"--debug-file", "/tmp/claude-debug"},
		{"--permission-mode", "bypassPermissions"},
		{"--permission-mode", "acceptEdits"},
		{"--allowedTools", "Bash"},
		{"--allowed-tools=Bash"},
		{"--tools", "Bash"},
		{"--output-format", "text"},
		{"--continue"},
		{"--resume", testSessionID},
		{"--no-session-persistence"},
		{"--model", "sonnet"},
		{"--model=sonnet"},
		{"--fallback-model", "haiku"},
		{"--effort", "high"},
		{"--debug"},
		{"--"},
		{"prompt"},
		{"--bg"},
		{"--exec", "command"},
		{"--session-id", "NOT-A-CANONICAL-UUID"},
		{"--session-id", testSessionID},
		{"--session-id=" + testSessionID},
	} {
		if _, err := BuildCommandSpec(mustTestExecutable(t), args, testWorkdir, bytes.NewReader(make([]byte, 16))); err == nil {
			t.Fatalf("BuildCommandSpec(%#v) error = nil", args)
		}
	}
	if _, err := BuildCommandSpec("claude", nil, testWorkdir, bytes.NewReader(make([]byte, 16))); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("relative executable error = %v", err)
	}
}

func TestBuildCommandSpecPinsResolvedExecutableAndStartRejectsReplacement(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "claude-first")
	second := filepath.Join(directory, "claude-second")
	link := filepath.Join(directory, "claude-link")
	if err := os.WriteFile(first, []byte("first"), 0o700); err != nil {
		t.Fatalf("write first executable: %v", err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o700); err != nil {
		t.Fatalf("write second executable: %v", err)
	}
	if err := os.Symlink(first, link); err != nil {
		t.Fatalf("create executable symlink: %v", err)
	}
	spec, err := BuildCommandSpec(link, nil, directory, bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatalf("BuildCommandSpec() error = %v", err)
	}
	resolvedFirst, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatalf("resolve first executable: %v", err)
	}
	if spec.Path != resolvedFirst {
		t.Fatalf("resolved path = %q, want %q", spec.Path, resolvedFirst)
	}
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove old symlink: %v", err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatalf("retarget executable symlink: %v", err)
	}
	if spec.Path != resolvedFirst {
		t.Fatalf("retarget changed pinned path to %q", spec.Path)
	}

	replacement := filepath.Join(directory, "replacement")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o700); err != nil {
		t.Fatalf("write replacement executable: %v", err)
	}
	if err := os.Remove(first); err != nil {
		t.Fatalf("remove pinned executable: %v", err)
	}
	if err := os.Rename(replacement, first); err != nil {
		t.Fatalf("replace pinned executable: %v", err)
	}
	if spec.ExecutableMatches() {
		t.Fatal("ExecutableMatches() = true after pinned executable replacement")
	}
}

func mustTestExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	return executable
}

func TestAdapterToolLoopEmitsCommentaryAndCanonicalResultFinal(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "tool_1", "text": "hello"})
	_ = rig.readChildInput(t)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"accepted","request_id":"tool_1"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"tool-1","name":"Read"}]},"session_id":"`+testSessionID+`"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"tool_1","kind":"commentary","text":"checking"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"tool_1","kind":"commentary","text":"tool: Read"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]},"session_id":"`+testSessionID+`","isReplay":false}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"assistant copy differs"}]},"session_id":"`+testSessionID+`"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"tool_1","kind":"commentary","text":"assistant copy differs"}`)
	rig.sendClaude(t, `{"type":"result","subtype":"success","is_error":false,"result":"canonical answer","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"final","request_id":"tool_1","text":"canonical answer"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"tool_1","status":"completed"}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "close"})
	rig.wantRun(t, nil)
}

func TestAdapterCarriesExactDurableMessageIDToClaudeAndRequiresItsReplay(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{
		"protocol": 1, "type": "submit", "request_id": "correlated_1",
		"message_id": "telegram-update:77", "text": "same text",
	})
	rig.wantChildInput(t, `{"type":"user","uuid":"telegram-update:77","message":{"role":"user","content":[{"type":"text","text":"same text"}]}}`)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"same text"}]},"session_id":"`+testSessionID+`","uuid":"telegram-update:77","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"accepted","request_id":"correlated_1","message_id":"telegram-update:77"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"session_id":"`+testSessionID+`"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"correlated_1","kind":"commentary","text":"done"}`)
	rig.sendClaude(t, `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"final","request_id":"correlated_1","text":"done"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"correlated_1","status":"completed"}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "close"})
	rig.wantRun(t, nil)
}

func TestAdapterRejectsSameTextReplayForDifferentDurableMessageID(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{
		"protocol": 1, "type": "submit", "request_id": "correlated_2",
		"message_id": "telegram-update:new", "text": "same text",
	})
	_ = rig.readChildInput(t)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"same text"}]},"session_id":"`+testSessionID+`","uuid":"telegram-update:old","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"correlated_2","status":"failed","error_code":"protocol_error"}`)
	rig.wantRun(t, ErrAdapterProtocol)
}

func TestAdapterFailsClosedForStructuredAttachmentWithoutOfficialClaudeProtocol(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{
		"protocol": 1, "type": "submit", "request_id": "unsupported_image", "message_id": "telegram:photo:claude", "text": "inspect",
		"attachments": []map[string]any{{
			"path": "/private/tmp/private-photo.png", "size": 3,
			"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	})
	rig.wantRun(t, ErrAdapterProtocol)
}

func TestAdapterRelaysTypedClaudePermissionAndCorrelatedDecision(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "permission_turn", "message_id": "telegram:43", "text": "run"})
	rig.wantChildInput(t, `{"type":"user","uuid":"telegram:43","message":{"role":"user","content":[{"type":"text","text":"run"}]}}`)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"run"}]},"session_id":"`+testSessionID+`","uuid":"telegram:43","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"accepted","request_id":"permission_turn","message_id":"telegram:43"}`)
	rig.sendClaude(t, `{"type":"control_request","request_id":"permission-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"go test ./...","description":"run tests","secret":"redact-me"},"tool_use_id":"tool-1","title":"Run command"}}`)
	rig.wantOutput(t, `{"protocol":1,"type":"interaction_request","request_id":"permission_turn","interaction_request":{"id":"permission-1","kind":"command_approval","turn_id":"permission_turn","item_id":"tool-1","reason":"run tests","command":"go test ./...","decisions":["accept","decline","cancel"]}}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "interaction_response", "request_id": "permission_turn", "interaction_response": map[string]any{"id": "permission-1", "outcome": "answered", "decision": "accept"}})
	rig.wantChildInput(t, `{"type":"control_response","response":{"subtype":"success","request_id":"permission-1","response":{"behavior":"allow"}}}`)
	rig.wantOutput(t, `{"protocol":1,"type":"interaction_response_accepted","provider_session_id":"`+testSessionID+`","request_id":"permission_turn","message_id":"telegram:43","interaction_id":"permission-1"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"session_id":"`+testSessionID+`"}`)
	rig.sendClaude(t, `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"permission_turn","kind":"commentary","text":"done"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"final","request_id":"permission_turn","text":"done"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"permission_turn","status":"completed"}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "close"})
	rig.wantRun(t, nil)
}

func TestAdapterIgnoresLateInterruptForCompletedRequest(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "done_1", "text": "hello"})
	_ = rig.readChildInput(t)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	_ = rig.readOutput(t)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"answer"}]},"session_id":"`+testSessionID+`"}`)
	rig.sendClaude(t, `{"type":"result","is_error":false,"result":"answer","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	_ = rig.readOutput(t)
	_ = rig.readOutput(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "interrupt", "request_id": "done_1"})
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "close"})
	rig.wantRun(t, nil)
}

func TestAdapterRejectsInitBeforeSubmit(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.wantRun(t, ErrAdapterProtocol)
}

func TestParentWireRejectsInvalidUTF8(t *testing.T) {
	line := append([]byte(`{"protocol":1,"type":"submit","request_id":"ok","text":"`), 0xff)
	line = append(line, []byte(`"}`)...)
	if _, err := decodeParentCommand(line, 100); !errors.Is(err, ErrAdapterProtocol) {
		t.Fatalf("decodeParentCommand() error = %v, want %v", err, ErrAdapterProtocol)
	}
}

func TestAdapterRejectsForgedBuiltCommand(t *testing.T) {
	spec, err := BuildCommandSpec(mustTestExecutable(t), nil, testWorkdir, bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatalf("BuildCommandSpec() error = %v", err)
	}
	spec.Args = append([]string{"--"}, spec.Args...)
	if _, err := NewAdapter(spec, io.NopCloser(strings.NewReader("")), io.Discard, fakeFactory{process: newFakeChild()}, AdapterOptions{}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("NewAdapter() error = %v, want %v", err, ErrInvalidCommand)
	}
}

func TestAdapterRejectsEscapedFinalThatWouldExceedOutboundLine(t *testing.T) {
	rig := newAdapterRigWithOptions(t, AdapterOptions{MaxLineBytes: 450, MaxTextBytes: 1024, StopTimeout: time.Second})
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "large_1", "text": "hello"})
	_ = rig.readChildInput(t)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	_ = rig.readOutput(t)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1"}]},"session_id":"`+testSessionID+`"}`)
	large := strings.Repeat("\u2028", 80)
	rig.sendClaude(t, `{"type":"result","is_error":false,"result":"`+large+`","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"large_1","status":"failed","error_code":"provider_error"}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "close"})
	rig.wantRun(t, nil)
}

func TestAdapterDoesNotPublishSuccessAfterObservedChildExit(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "exit_1", "text": "hello"})
	_ = rig.readChildInput(t)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	_ = rig.readOutput(t)
	rig.process.exit(errors.New("provider exited"))
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"exit_1","status":"failed","error_code":"transport_error"}`)
	rig.wantRun(t, ErrChildExit)
}

func TestAdapterDoesNotPublishSuccessWhenChildExitsDuringCommentary(t *testing.T) {
	spec, err := BuildCommandSpec(mustTestExecutable(t), nil, testWorkdir, bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatalf("BuildCommandSpec() error = %v", err)
	}
	parentRead, parentWrite := io.Pipe()
	outputRead, outputWrite := io.Pipe()
	process := newFakeChild()
	adapter, err := NewAdapter(spec, parentRead, &exitOnCommentaryWriter{writer: outputWrite, process: process}, fakeFactory{process: process}, AdapterOptions{StopTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	rig := &adapterRig{spec: spec, process: process, parentWrite: parentWrite, outputRead: bufio.NewReader(outputRead), runDone: make(chan error, 1)}
	defer func() {
		rig.cleanup()
		_ = outputRead.Close()
		_ = outputWrite.Close()
	}()
	go func() { rig.runDone <- adapter.Run(context.Background()) }()
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "race_1", "text": "hello"})
	_ = rig.readChildInput(t)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	_ = rig.readOutput(t)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"commentary"}]},"session_id":"`+testSessionID+`"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"answer"}]},"session_id":"`+testSessionID+`"}`)
	rig.sendClaude(t, `{"type":"result","is_error":false,"result":"answer","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"race_1","kind":"commentary","text":"commentary"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"race_1","status":"failed","error_code":"transport_error"}`)
	rig.wantRun(t, ErrChildExit)
}

func TestAdapterSuccessfulTurnUsesExactWireAndStaysAliveUntilClose(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()

	rig.start(t)
	rig.wantOutput(t, `{"protocol":1,"type":"ready","provider_session_id":"`+testSessionID+`","readiness":"protocol","authentication":"unknown"}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "req_1", "text": "hello"})
	rig.wantChildInput(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"accepted","request_id":"req_1"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"answer"}]},"session_id":"`+testSessionID+`"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"req_1","kind":"commentary","text":"answer"}`)
	rig.sendClaude(t, `{"type":"result","subtype":"success","is_error":false,"result":"answer","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"final","request_id":"req_1","text":"answer"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"req_1","status":"completed"}`)

	select {
	case err := <-rig.runDone:
		t.Fatalf("adapter exited before close: %v", err)
	default:
	}
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "req_2", "text": "again"})
	rig.wantChildInput(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"again"}]}}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"again"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"accepted","request_id":"req_2"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second"}]},"session_id":"`+testSessionID+`"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"req_2","kind":"commentary","text":"second"}`)
	rig.sendClaude(t, `{"type":"result","subtype":"success","is_error":false,"result":"second","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"final","request_id":"req_2","text":"second"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"req_2","status":"completed"}`)

	rig.sendParent(t, map[string]any{"protocol": 1, "type": "close"})
	rig.wantRun(t, nil)
	if !rig.process.wasKilled() {
		t.Fatal("close did not terminate child")
	}
}

func TestAdapterQueuesSecondSubmitUntilActiveTurnCompletes(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)

	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "first_1", "text": "first"})
	rig.wantChildInput(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"first"}]}}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "second_2", "text": "second"})

	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"first"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"accepted","request_id":"first_1"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"one"}]},"session_id":"`+testSessionID+`"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"first_1","kind":"commentary","text":"one"}`)
	rig.sendClaude(t, `{"type":"result","is_error":false,"result":"one","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"final","request_id":"first_1","text":"one"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"first_1","status":"completed"}`)

	rig.wantChildInput(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"second"}]}}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"second"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"accepted","request_id":"second_2"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"two"}]},"session_id":"`+testSessionID+`"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"second_2","kind":"commentary","text":"two"}`)
	rig.sendClaude(t, `{"type":"result","is_error":false,"result":"two","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"final","request_id":"second_2","text":"two"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"second_2","status":"completed"}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "close"})
	rig.wantRun(t, nil)
}

func TestAdapterAdvancesQueuedSubmitAfterProviderFailure(t *testing.T) {
	rig := newAdapterRigWithOptions(t, AdapterOptions{MaxTextBytes: 8, StopTimeout: time.Second})
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "first_1", "text": "first"})
	_ = rig.readChildInput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "second_2", "text": "second"})
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"first"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	_ = rig.readOutput(t)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1"}]},"session_id":"`+testSessionID+`"}`)
	rig.sendClaude(t, `{"type":"result","is_error":false,"result":"too-large","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"first_1","status":"failed","error_code":"provider_error"}`)
	rig.wantChildInput(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"second"}]}}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"second"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	_ = rig.readOutput(t)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]},"session_id":"`+testSessionID+`"}`)
	_ = rig.readOutput(t)
	rig.sendClaude(t, `{"type":"result","is_error":false,"result":"ok","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	_ = rig.readOutput(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "close"})
	rig.wantRun(t, nil)
}

func TestAdapterPreservesAuthenticationFailureWhenClaudeExitsAfterResult(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	rig.start(t)
	rig.wantOutput(t, `{"protocol":1,"type":"ready","provider_session_id":"`+testSessionID+`","readiness":"protocol","authentication":"unknown"}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "auth_1", "text": "hello"})
	rig.wantChildInput(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"accepted","request_id":"auth_1"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Not logged in"}]},"session_id":"`+testSessionID+`","error":"authentication_failed"}`)
	rig.sendClaude(t, `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.process.exit(errors.New("claude exits after auth failure"))
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"auth_1","status":"failed","error_code":"authentication_failed"}`)
	rig.wantRun(t, nil)
}

func TestParentWireRequiresExactKeysAndBoundedASCIIRequestID(t *testing.T) {
	valid := []byte(`{"protocol":1,"type":"submit","request_id":"A_z-9","text":"hello"}`)
	command, err := decodeParentCommand(valid, 5)
	if err != nil || command.kind != commandSubmit || command.requestID != "A_z-9" || command.text != "hello" {
		t.Fatalf("valid command = %#v, error=%v", command, err)
	}

	for _, line := range []string{
		`{"protocol":2,"type":"close"}`,
		`{"protocol":1,"type":"close","extra":true}`,
		`{"protocol":1,"protocol":1,"type":"close"}`,
		`{"protocol":1,"type":"submit","request_id":"contains space","text":"hello"}`,
		`{"protocol":1,"type":"submit","request_id":"unicode_я","text":"hello"}`,
		`{"protocol":1,"type":"submit","request_id":"ok","text":"123456"}`,
		`{"protocol":1,"type":"interrupt","request_id":""}`,
	} {
		if _, err := decodeParentCommand([]byte(line), 5); !errors.Is(err, ErrAdapterProtocol) {
			t.Fatalf("decodeParentCommand(%s) error = %v", line, err)
		}
	}
}

func TestAdapterSanitizesStartAndParentTransportErrors(t *testing.T) {
	spec, err := BuildCommandSpec(
		mustTestExecutable(t),
		nil,
		testWorkdir,
		bytes.NewReader(make([]byte, 16)),
	)
	if err != nil {
		t.Fatalf("BuildCommandSpec() error = %v", err)
	}
	const secret = "secret=/private/token"

	adapter, err := NewAdapter(
		spec,
		io.NopCloser(strings.NewReader("")),
		io.Discard,
		startErrorFactory{err: errors.New(secret)},
		AdapterOptions{},
	)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	assertAdapterErrorSanitized(t, adapter.Run(context.Background()), ErrChildStart, secret)

	process := newFakeChild()
	adapter, err = NewAdapter(
		spec,
		errorReadCloser{err: errors.New(secret)},
		io.Discard,
		fakeFactory{process: process},
		AdapterOptions{StopTimeout: time.Second},
	)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	assertAdapterErrorSanitized(t, adapter.Run(context.Background()), ErrAdapterTransport, secret)
}

func TestAdapterAuthenticationFailureIsAcceptedButHasNoFinal(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "auth_1", "text": "hello"})
	_ = rig.readChildInput(t)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"`+testWorkdir+`","session_id":"`+testSessionID+`","claude_code_version":"2.1.181"}`)
	rig.sendClaude(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]},"session_id":"`+testSessionID+`","isReplay":true}`)
	rig.wantOutput(t, `{"protocol":1,"type":"accepted","request_id":"auth_1"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"must remain buffered on failure"}]},"session_id":"`+testSessionID+`"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"event","request_id":"auth_1","kind":"commentary","text":"must remain buffered on failure"}`)
	rig.sendClaude(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"provider detail"}]},"session_id":"`+testSessionID+`","error":"authentication_failed"}`)
	rig.sendClaude(t, `{"type":"result","subtype":"success","is_error":true,"result":"provider detail","session_id":"`+testSessionID+`","terminal_reason":"completed"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"auth_1","status":"failed","error_code":"authentication_failed"}`)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "close"})
	rig.wantRun(t, nil)
}

func TestAdapterRejectsInitIdentityMismatchWithoutAccepted(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "bad_1", "text": "hello"})
	_ = rig.readChildInput(t)
	rig.sendClaude(t, `{"type":"system","subtype":"init","cwd":"/wrong","session_id":"22222222-2222-4222-8222-222222222222","claude_code_version":"2.1.181"}`)
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"bad_1","status":"failed","error_code":"protocol_error"}`)
	rig.wantRun(t, ErrAdapterProtocol)
	if !rig.process.wasKilled() {
		t.Fatal("identity mismatch did not terminate child")
	}
}

func TestAdapterInterruptTerminatesAndConfirmsBeforeCompletion(t *testing.T) {
	rig := newAdapterRig(t)
	defer rig.cleanup()
	rig.start(t)
	_ = rig.readOutput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "submit", "request_id": "stop_1", "text": "hello"})
	_ = rig.readChildInput(t)
	rig.sendParent(t, map[string]any{"protocol": 1, "type": "interrupt", "request_id": "stop_1"})
	rig.wantOutput(t, `{"protocol":1,"type":"completed","request_id":"stop_1","status":"interrupted","error_code":"interrupted"}`)
	if !rig.process.wasKilled() {
		t.Fatal("completion arrived before confirmed child termination")
	}
	rig.wantRun(t, nil)
}

type adapterRig struct {
	spec        CommandSpec
	process     *fakeChild
	parentWrite *io.PipeWriter
	outputRead  *bufio.Reader
	runDone     chan error
}

func newAdapterRig(t *testing.T) *adapterRig {
	return newAdapterRigWithOptions(t, AdapterOptions{StopTimeout: time.Second})
}

func newAdapterRigWithOptions(t *testing.T, options AdapterOptions) *adapterRig {
	t.Helper()
	spec, err := BuildCommandSpec(
		mustTestExecutable(t),
		nil,
		testWorkdir,
		bytes.NewReader(make([]byte, 16)),
	)
	if err != nil {
		t.Fatalf("BuildCommandSpec() error = %v", err)
	}
	parentRead, parentWrite := io.Pipe()
	outputRead, outputWrite := io.Pipe()
	process := newFakeChild()
	adapter, err := NewAdapter(
		spec,
		parentRead,
		outputWrite,
		fakeFactory{process: process},
		options,
	)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	rig := &adapterRig{
		spec:        spec,
		process:     process,
		parentWrite: parentWrite,
		outputRead:  bufio.NewReader(outputRead),
		runDone:     make(chan error, 1),
	}
	t.Cleanup(func() {
		_ = outputRead.Close()
		_ = outputWrite.Close()
	})
	go func() { rig.runDone <- adapter.Run(context.Background()) }()
	return rig
}

func (rig *adapterRig) start(t *testing.T) { t.Helper() }

func (rig *adapterRig) sendParent(t *testing.T, value any) {
	t.Helper()
	if err := json.NewEncoder(rig.parentWrite).Encode(value); err != nil {
		t.Fatalf("send parent: %v", err)
	}
}

func (rig *adapterRig) sendClaude(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(rig.process.providerOutput, line+"\n"); err != nil {
		t.Fatalf("send Claude event: %v", err)
	}
}

func (rig *adapterRig) readOutput(t *testing.T) string {
	t.Helper()
	return readLineWithTimeout(t, rig.outputRead)
}

func (rig *adapterRig) wantOutput(t *testing.T, want string) {
	t.Helper()
	if got := rig.readOutput(t); got != want {
		t.Fatalf("adapter output = %s, want %s", got, want)
	}
}

func (rig *adapterRig) readChildInput(t *testing.T) string {
	t.Helper()
	return readLineWithTimeout(t, rig.process.childInput)
}

func (rig *adapterRig) wantChildInput(t *testing.T, want string) {
	t.Helper()
	if got := rig.readChildInput(t); got != want {
		t.Fatalf("child input = %s, want %s", got, want)
	}
}

func (rig *adapterRig) wantRun(t *testing.T, want error) {
	t.Helper()
	select {
	case err := <-rig.runDone:
		if !errors.Is(err, want) {
			t.Fatalf("Run() error = %v, want %v", err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not return")
	}
}

func (rig *adapterRig) cleanup() {
	_ = rig.parentWrite.Close()
	_ = rig.process.Kill()
}

func readLineWithTimeout(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		done <- result{line: strings.TrimSuffix(line, "\n"), err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("read line: %v", result.err)
		}
		return result.line
	case <-time.After(time.Second):
		t.Fatal("timed out reading line")
		return ""
	}
}

type fakeFactory struct{ process *fakeChild }

func (factory fakeFactory) Start(context.Context, CommandSpec) (ChildProcess, error) {
	return factory.process, nil
}

type startErrorFactory struct{ err error }

func (factory startErrorFactory) Start(context.Context, CommandSpec) (ChildProcess, error) {
	return nil, factory.err
}

type errorReadCloser struct{ err error }

func (reader errorReadCloser) Read([]byte) (int, error) { return 0, reader.err }
func (errorReadCloser) Close() error                    { return nil }

type exitOnCommentaryWriter struct {
	writer  io.Writer
	process *fakeChild
}

func (writer *exitOnCommentaryWriter) Write(document []byte) (int, error) {
	written, err := writer.writer.Write(document)
	if err == nil && bytes.Contains(document, []byte(`"type":"event"`)) {
		writer.process.exit(errors.New("provider exited during commentary"))
	}
	return written, err
}

func assertAdapterErrorSanitized(t *testing.T, err, want error, secret string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked transport detail: %v", err)
	}
}

type fakeChild struct {
	adapterInput   *io.PipeWriter
	childInput     *bufio.Reader
	adapterOutput  *io.PipeReader
	providerOutput *io.PipeWriter
	done           chan error
	killed         chan struct{}
	once           sync.Once
}

func newFakeChild() *fakeChild {
	childInput, adapterInput := io.Pipe()
	adapterOutput, providerOutput := io.Pipe()
	return &fakeChild{
		adapterInput:   adapterInput,
		childInput:     bufio.NewReader(childInput),
		adapterOutput:  adapterOutput,
		providerOutput: providerOutput,
		done:           make(chan error, 1),
		killed:         make(chan struct{}),
	}
}

func (child *fakeChild) Stdin() io.WriteCloser { return child.adapterInput }
func (child *fakeChild) Stdout() io.ReadCloser { return child.adapterOutput }
func (child *fakeChild) Done() <-chan error    { return child.done }
func (child *fakeChild) Kill() error {
	child.once.Do(func() {
		close(child.killed)
		_ = child.adapterInput.Close()
		_ = child.adapterOutput.Close()
		_ = child.providerOutput.Close()
		child.done <- errors.New("killed")
		close(child.done)
	})
	return nil
}

func (child *fakeChild) exit(err error) {
	child.once.Do(func() {
		child.done <- err
		close(child.done)
	})
}
func (child *fakeChild) wasKilled() bool {
	select {
	case <-child.killed:
		return true
	default:
		return false
	}
}
