package authcomposition_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"bria/internal/authcomposition"
	"bria/internal/domain"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
)

const (
	testOwnerID = int64(42)
	testChatID  = int64(42)
)

func TestOpenComposesDurableExactLocalCodexAndClaudeAuthorization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("provider CLI fixtures are Unix shell scripts")
	}
	directory := t.TempDir()
	storePath := filepath.Join(directory, "state", "authorization.json")
	codexMarker := filepath.Join(directory, "codex-authenticated")
	codexPath := writeExecutable(t, directory, "codex", `#!/bin/sh
case "$1:$2" in
  login:status) [ -f `+shellQuote(codexMarker)+` ] ;;
  login:--with-api-key)
    IFS= read -r candidate || exit 21
    [ "$candidate" = "sk-codex-transient" ] || exit 22
    (umask 077 && : > `+shellQuote(codexMarker)+`) || exit 23
    ;;
  *) exit 24 ;;
esac
`)
	claudePath := writeExecutable(t, directory, "claude", `#!/bin/sh
[ "$1" = "--bare" ] || exit 31
[ "$ANTHROPIC_API_KEY" = "sk-ant-transient" ] || exit 32
IFS= read -r prompt || exit 33
[ -n "$prompt" ] || exit 34
printf '%s\n' '{"type":"result","is_error":false,"terminal_reason":"completed"}'
`)
	claudeCredentialPath := filepath.Join(directory, "credentials", "claude.json")
	deleter := &recordingTelegramDeleter{}
	flow, err := authcomposition.Open(authcomposition.Options{
		OwnerID: testOwnerID, LocalComputerID: "local", StorePath: storePath,
		Telegram: deleter, CodexExecutable: codexPath, ClaudeExecutable: claudePath,
		ClaudeCredentialPath: claudeCredentialPath,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	var contract telegramcontroller.AuthorizationFlow = flow
	if !contract.SupportsAuthorization(domain.ProviderCodex) || !contract.SupportsAuthorization(domain.ProviderClaude) ||
		contract.SupportsAuthorization(domain.Provider("future")) {
		t.Fatal("SupportsAuthorization() did not reflect exact composed capabilities")
	}

	codexStart := telegramcontroller.AuthorizationStart{
		OperationID: "telegram-update:100:authorization", ActorID: testOwnerID, PrivateChatID: testChatID,
		ConversationKind: "private", ComputerID: "local", Provider: domain.ProviderCodex,
	}
	codexChallenge, err := contract.StartAuthorization(context.Background(), codexStart)
	if err != nil {
		t.Fatalf("StartAuthorization(codex) error = %v", err)
	}
	assertChallengeBinding(t, codexChallenge, codexStart)
	pending, err := contract.PendingAuthorizations(context.Background(), telegramcontroller.AuthorizationPendingLookup{
		ActorID: testOwnerID, PrivateChatID: testChatID, ConversationKind: "private",
	})
	if err != nil || len(pending) != 1 || pending[0].OperationID != codexStart.OperationID || !pending[0].AcceptsSecret {
		t.Fatalf("PendingAuthorizations() = %#v, %v", pending, err)
	}
	codexSecret := []byte("sk-codex-transient")
	result, err := contract.SubmitAuthorization(context.Background(), telegramcontroller.AuthorizationSecret{
		OperationID: codexStart.OperationID, SubmissionOperationID: "telegram-message:9001:authorization",
		ActorID: testOwnerID, PrivateChatID: testChatID, ConversationKind: "private", SourceMessageID: 9001,
		ComputerID: "local", Provider: domain.ProviderCodex, ChallengeReference: codexChallenge.ChallengeReference,
		Secret: codexSecret,
	})
	if err != nil || !result.Authenticated || !result.DeletionKnown {
		t.Fatalf("SubmitAuthorization(codex) = %#v, %v", result, err)
	}
	assertZeroed(t, codexSecret)

	claudeStart := codexStart
	claudeStart.OperationID = "telegram-update:101:authorization"
	claudeStart.Provider = domain.ProviderClaude
	claudeChallenge, err := contract.StartAuthorization(context.Background(), claudeStart)
	if err != nil {
		t.Fatalf("StartAuthorization(claude) error = %v", err)
	}
	assertChallengeBinding(t, claudeChallenge, claudeStart)
	claudeSecret := []byte("sk-ant-transient")
	result, err = contract.SubmitAuthorization(context.Background(), telegramcontroller.AuthorizationSecret{
		OperationID: claudeStart.OperationID, SubmissionOperationID: "telegram-message:9002:authorization",
		ActorID: testOwnerID, PrivateChatID: testChatID, ConversationKind: "private", SourceMessageID: 9002,
		ComputerID: "local", Provider: domain.ProviderClaude, ChallengeReference: claudeChallenge.ChallengeReference,
		Secret: claudeSecret,
	})
	if err != nil || !result.Authenticated || !result.DeletionKnown {
		t.Fatalf("SubmitAuthorization(claude) = %#v, %v", result, err)
	}
	assertZeroed(t, claudeSecret)
	if len(deleter.requests) != 2 || deleter.requests[0].MessageID != 9001 || deleter.requests[1].MessageID != 9002 {
		t.Fatalf("Telegram deletions = %#v", deleter.requests)
	}
	state, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), "sk-codex-transient") || strings.Contains(string(state), "sk-ant-transient") {
		t.Fatal("authorization store contains a submitted secret")
	}

	reopened, err := authcomposition.Open(authcomposition.Options{
		OwnerID: testOwnerID, LocalComputerID: "local", StorePath: storePath,
		Telegram: &recordingTelegramDeleter{}, CodexExecutable: codexPath, ClaudeExecutable: claudePath,
		ClaudeCredentialPath: claudeCredentialPath,
	})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	consumed, err := reopened.ConsumeAuthorizationMessage(context.Background(), telegramcontroller.AuthorizationMessageLookup{
		ActorID: testOwnerID, PrivateChatID: testChatID, ConversationKind: "private", SourceMessageID: 9001,
	})
	if err != nil || !consumed.Bound || consumed.Provider != domain.ProviderCodex || !consumed.Authenticated || !consumed.DeletionKnown {
		t.Fatalf("ConsumeAuthorizationMessage(redelivery) = %#v, %v", consumed, err)
	}
	pending, err = reopened.PendingAuthorizations(context.Background(), telegramcontroller.AuthorizationPendingLookup{
		ActorID: testOwnerID, PrivateChatID: testChatID, ConversationKind: "private",
	})
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after reopen = %#v, %v", pending, err)
	}
}

func TestPendingAndDiscardDeletionRecoverDurablyWithoutSecretBody(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("provider CLI fixture is a Unix shell script")
	}
	directory := t.TempDir()
	storePath := filepath.Join(directory, "state", "authorization.json")
	codexPath := writeExecutable(t, directory, "codex", "#!/bin/sh\nexit 1\n")
	failingDelete := &recordingTelegramDeleter{err: errors.New("ambiguous Telegram failure containing possible-secret")}
	flow, err := authcomposition.Open(authcomposition.Options{
		OwnerID: testOwnerID, LocalComputerID: "local", StorePath: storePath,
		Telegram: failingDelete, CodexExecutable: codexPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := telegramcontroller.AuthorizationDiscard{
		OperationID: "telegram-message:9100:delete", ActorID: testOwnerID, PrivateChatID: testChatID,
		ConversationKind: "private", SourceMessageID: 9100,
	}
	result, err := flow.DiscardAuthorizationMessage(context.Background(), request)
	if err == nil || result.DeletionKnown || strings.Contains(err.Error(), "possible-secret") {
		t.Fatalf("first discard = %#v, %v", result, err)
	}

	succeedingDelete := &recordingTelegramDeleter{}
	reopened, err := authcomposition.Open(authcomposition.Options{
		OwnerID: testOwnerID, LocalComputerID: "local", StorePath: storePath,
		Telegram: succeedingDelete, CodexExecutable: codexPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := reopened.ConsumeAuthorizationMessage(context.Background(), telegramcontroller.AuthorizationMessageLookup{
		ActorID: testOwnerID, PrivateChatID: testChatID, ConversationKind: "private", SourceMessageID: 9100,
	})
	if err != nil || !consumed.Bound || consumed.Authenticated || !consumed.DeletionKnown || len(succeedingDelete.requests) != 1 || succeedingDelete.requests[0].MessageID != 9100 {
		t.Fatalf("recovered consume = %#v, %v, deletes=%#v", consumed, err, succeedingDelete.requests)
	}
	state, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), "possible-secret") {
		t.Fatal("discard persistence contains a possible secret body or provider error")
	}
}

func TestAlreadyDeletedTelegramMessageConfirmsDurableDiscardIdempotently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("provider CLI fixture is a Unix shell script")
	}
	directory := t.TempDir()
	codexPath := writeExecutable(t, directory, "codex", "#!/bin/sh\nexit 1\n")
	client, err := telegram.NewClient("test-token", httpClientFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"ok":false,"error_code":400,"description":"Bad Request: message to delete not found"}`)),
		}, nil
	}), telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	flow, err := authcomposition.Open(authcomposition.Options{
		OwnerID: testOwnerID, LocalComputerID: "local", StorePath: filepath.Join(directory, "authorization.json"),
		Telegram: client, CodexExecutable: codexPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := flow.DiscardAuthorizationMessage(context.Background(), telegramcontroller.AuthorizationDiscard{
		OperationID: "telegram-message:9200:delete", ActorID: testOwnerID, PrivateChatID: testChatID,
		ConversationKind: "private", SourceMessageID: 9200,
	})
	if err != nil || !result.DeletionKnown {
		t.Fatalf("DiscardAuthorizationMessage(already absent) = %#v, %v", result, err)
	}
}

func TestFlowRejectsForeignComputerAndIncompleteProviderConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("provider CLI fixture is a Unix shell script")
	}
	directory := t.TempDir()
	codexPath := writeExecutable(t, directory, "codex", "#!/bin/sh\nexit 1\n")
	base := authcomposition.Options{
		OwnerID: testOwnerID, LocalComputerID: "local", StorePath: filepath.Join(directory, "auth.json"),
		Telegram: &recordingTelegramDeleter{}, CodexExecutable: codexPath,
	}
	flow, err := authcomposition.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	_, err = flow.StartAuthorization(context.Background(), telegramcontroller.AuthorizationStart{
		OperationID: "telegram-update:103:authorization", ActorID: testOwnerID, PrivateChatID: testChatID,
		ConversationKind: "private", ComputerID: "foreign", Provider: domain.ProviderCodex,
	})
	if err == nil {
		t.Fatal("foreign computer authorization was accepted")
	}
	challenge, err := flow.StartAuthorization(context.Background(), telegramcontroller.AuthorizationStart{
		OperationID: "telegram-update:104:authorization", ActorID: testOwnerID, PrivateChatID: testChatID,
		ConversationKind: "private", ComputerID: "local", Provider: domain.ProviderCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignSecret := []byte("foreign-computer-secret")
	result, err := flow.SubmitAuthorization(context.Background(), telegramcontroller.AuthorizationSecret{
		OperationID: challenge.OperationID, SubmissionOperationID: "telegram-message:9104:authorization",
		ActorID: testOwnerID, PrivateChatID: testChatID, ConversationKind: "private", SourceMessageID: 9104,
		ComputerID: "foreign", Provider: domain.ProviderCodex, ChallengeReference: challenge.ChallengeReference,
		Secret: foreignSecret,
	})
	if err == nil || result.Authenticated || !result.DeletionKnown {
		t.Fatalf("foreign computer submit = %#v, %v", result, err)
	}
	assertZeroed(t, foreignSecret)

	base.ClaudeExecutable = codexPath
	if _, err := authcomposition.Open(base); err == nil {
		t.Fatal("incomplete Claude configuration was accepted")
	}
	base.ClaudeExecutable = ""
	base.CodexExecutable = ""
	if _, err := authcomposition.Open(base); err == nil {
		t.Fatal("configuration without authorization providers was accepted")
	}
}

type recordingTelegramDeleter struct {
	requests []telegram.DeleteMessageRequest
	err      error
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (function httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func (deleter *recordingTelegramDeleter) DeleteMessage(_ context.Context, request telegram.DeleteMessageRequest) error {
	deleter.requests = append(deleter.requests, request)
	return deleter.err
}

func writeExecutable(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func assertChallengeBinding(t *testing.T, challenge telegramcontroller.AuthorizationChallenge, request telegramcontroller.AuthorizationStart) {
	t.Helper()
	if challenge.OperationID != request.OperationID || challenge.ComputerID != request.ComputerID || challenge.Provider != request.Provider ||
		challenge.ChallengeReference == "" || challenge.Instruction == "" {
		t.Fatalf("challenge = %#v, request = %#v", challenge, request)
	}
}

func assertZeroed(t *testing.T, value []byte) {
	t.Helper()
	for _, character := range value {
		if character != 0 {
			t.Fatalf("secret was retained after submission: %q", value)
		}
	}
}
