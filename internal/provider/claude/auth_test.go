package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bria/internal/authflow"
)

func TestAPIKeyAuthenticatorVerifiesThenAtomicallyPersistsAndReplays(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "credentials", "claude-api-key.json")
	verifier := &fakeAPIKeyVerifier{}
	authenticator, err := newAPIKeyAuthenticator(verifier, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	begin, err := authenticator.Begin(context.Background(), authflow.BeginRequest{
		OperationID: "operation-1", ComputerID: "local", Provider: authflow.ProviderClaude,
	})
	if err != nil || begin.ChallengeReference == "" || !begin.ExpiresAt.Equal(now.Add(15*time.Minute)) || !strings.Contains(begin.Instruction, "API key Claude") {
		t.Fatalf("Begin() = %#v, %v", begin, err)
	}
	secret := "sk-ant-test-secret"
	request := authflow.CompleteRequest{
		OperationID: "operation-1", ComputerID: "local", Provider: authflow.ProviderClaude,
		ChallengeReference: begin.ChallengeReference, Secret: authflow.NewSecretPayload([]byte(secret)),
	}
	if err := authenticator.Complete(context.Background(), request); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if verifier.calls != 1 || string(verifier.keys[0]) != secret {
		t.Fatalf("verifier calls=%d keys=%q", verifier.calls, verifier.keys)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o, want 600", info.Mode().Perm())
	}

	// Recreate the authenticator to prove replay comes from the physically
	// reread credential, not process memory.
	replayVerifier := &fakeAPIKeyVerifier{err: errors.New("must not be called")}
	reopened, err := newAPIKeyAuthenticator(replayVerifier, path, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Complete(context.Background(), request); err != nil {
		t.Fatalf("replayed Complete() error = %v", err)
	}
	if replayVerifier.calls != 0 {
		t.Fatalf("replay verifier calls = %d, want 0", replayVerifier.calls)
	}
}

func TestExactClaudeChildGetsStoredAPIKeyOnlyInItsEnvironmentAndBareMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials", "claude-api-key.json")
	authenticator, err := newAPIKeyAuthenticator(&fakeAPIKeyVerifier{}, path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := authenticator.Begin(context.Background(), authflow.BeginRequest{OperationID: "op", ComputerID: "local", Provider: authflow.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-ant-child-only"
	if err := authenticator.Complete(context.Background(), authflow.CompleteRequest{
		OperationID: "op", ComputerID: "local", Provider: authflow.ProviderClaude,
		ChallengeReference: begin.ChallengeReference, Secret: authflow.NewSecretPayload([]byte(secret)),
	}); err != nil {
		t.Fatal(err)
	}
	spec, err := BuildCommandSpec(mustTestExecutable(t), nil, t.TempDir(), bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	parent := []string{"HOME=/safe/home", "PATH=/safe/bin", "ANTHROPIC_API_KEY=parent-secret"}
	environment, err := spec.EnvironmentWithStoredAPIKey(parent, path)
	if err != nil {
		t.Fatalf("EnvironmentWithStoredAPIKey() error = %v", err)
	}
	if parent[2] != "ANTHROPIC_API_KEY=parent-secret" {
		t.Fatal("parent environment was mutated")
	}
	keys := 0
	for _, entry := range environment {
		if strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") {
			keys++
			if entry != "ANTHROPIC_API_KEY="+secret {
				t.Fatalf("child key entry = %q", entry)
			}
		}
	}
	if keys != 1 {
		t.Fatalf("child key entries = %d, want 1", keys)
	}
	if strings.Contains(strings.Join(spec.Args, "\x00"), secret) {
		t.Fatal("secret leaked to argv")
	}
	if !containsArgument(spec.Args, "--bare") {
		t.Fatalf("Claude args lack documented --bare mode: %#v", spec.Args)
	}
}

func TestPinnedVerifierDoesNotRetainParentAnthropicCredential(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "parent-must-not-be-retained")
	t.Setenv("TELEGRAM_BOT_TOKEN", "telegram-must-not-reach-provider")
	t.Setenv("BRIA_PRIVATE_STATE", "bria-must-not-reach-provider")
	verifier, err := newPinnedBareVerifier(mustTestExecutable(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range verifier.environment {
		if strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") || strings.HasPrefix(entry, "TELEGRAM_BOT_TOKEN=") || strings.HasPrefix(entry, "BRIA_") {
			t.Fatalf("pinned verifier retained parent-wide secret: %q", entry)
		}
	}
}

func TestAPIKeyAuthenticatorNeverPersistsOrLeaksRejectedOrUnconfirmedSecret(t *testing.T) {
	for _, tc := range []struct {
		name        string
		verifierErr error
		want        error
	}{
		{name: "authoritative rejection", verifierErr: fmt.Errorf("%w: leaked sk-ant-rejected", authflow.ErrProviderRejected), want: authflow.ErrProviderRejected},
		{name: "ambiguous failure", verifierErr: errors.New("timeout leaked sk-ant-rejected"), want: authflow.ErrAuthorizationUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials", "claude-api-key.json")
			authenticator, err := newAPIKeyAuthenticator(&fakeAPIKeyVerifier{err: tc.verifierErr}, path, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			begin, err := authenticator.Begin(context.Background(), authflow.BeginRequest{OperationID: "op", ComputerID: "local", Provider: authflow.ProviderClaude})
			if err != nil {
				t.Fatal(err)
			}
			secret := "sk-ant-rejected"
			err = authenticator.Complete(context.Background(), authflow.CompleteRequest{
				OperationID: "op", ComputerID: "local", Provider: authflow.ProviderClaude,
				ChallengeReference: begin.ChallengeReference, Secret: authflow.NewSecretPayload([]byte(secret)),
			})
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), secret) {
				t.Fatalf("Complete() error = %v, want sanitized %v", err, tc.want)
			}
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("credential exists after failed check: %v", statErr)
			}
		})
	}
}

func TestSameOperationCannotReplaceVerifiedCredentialOnReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials", "claude-api-key.json")
	verifier := &fakeAPIKeyVerifier{}
	authenticator, err := newAPIKeyAuthenticator(verifier, path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := authenticator.Begin(context.Background(), authflow.BeginRequest{OperationID: "op", ComputerID: "local", Provider: authflow.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	complete := func(secret string) error {
		return authenticator.Complete(context.Background(), authflow.CompleteRequest{
			OperationID: "op", ComputerID: "local", Provider: authflow.ProviderClaude,
			ChallengeReference: begin.ChallengeReference, Secret: authflow.NewSecretPayload([]byte(secret)),
		})
	}
	if err := complete("sk-ant-original"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := complete("sk-ant-replacement"); !errors.Is(err, authflow.ErrAuthorizationUnconfirmed) {
		t.Fatalf("replacement replay error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || verifier.calls != 1 {
		t.Fatalf("same operation changed credential or reverified: calls=%d", verifier.calls)
	}
}

func TestMalformedCompletionIsRejectedBeforeProviderOrCredentialWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials", "claude-api-key.json")
	verifier := &fakeAPIKeyVerifier{}
	authenticator, err := newAPIKeyAuthenticator(verifier, path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	err = authenticator.Complete(context.Background(), authflow.CompleteRequest{
		Provider: authflow.ProviderClaude, ChallengeReference: claudeAPIKeyChallenge("", ""),
		Secret: authflow.NewSecretPayload([]byte("sk-ant-should-not-run")),
	})
	if !errors.Is(err, authflow.ErrAuthorizationUnconfirmed) || verifier.calls != 0 {
		t.Fatalf("Complete() = %v, verifier calls=%d", err, verifier.calls)
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("malformed completion wrote credential: %v", statErr)
	}
}

func TestPinnedBareVerifierUsesOnlyDocumentedAPIKeyMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	directory := t.TempDir()
	capture := filepath.Join(directory, "verified")
	originalHome := t.TempDir()
	t.Setenv("HOME", originalHome)
	script := filepath.Join(directory, "claude")
	body := `#!/bin/sh
[ "$1" = "--bare" ] || exit 11
[ "$2" = "--print" ] || exit 12
[ "$3" = "--input-format" ] || exit 13
[ "$4" = "stream-json" ] || exit 14
[ "$5" = "--output-format" ] || exit 15
[ "$6" = "stream-json" ] || exit 16
[ "$7" = "--tools" ] || exit 17
[ "$8" = "" ] || exit 18
[ "$9" = "--max-turns" ] || exit 19
[ "${10}" = "1" ] || exit 20
[ "${11}" = "--no-session-persistence" ] || exit 21
[ "$#" = "11" ] || exit 22
[ "$ANTHROPIC_API_KEY" = "sk-ant-exact-child" ] || exit 16
[ -n "$HOME" ] && [ "$HOME" != "$CLAUDE_AUTH_TEST_ORIGINAL_HOME" ] && [ -d "$HOME" ] || exit 25
[ -n "$CLAUDE_CONFIG_DIR" ] && [ -d "$CLAUDE_CONFIG_DIR" ] || exit 26
[ "$PWD" -ef "$HOME" ] || exit 27
IFS= read -r prompt || exit 23
[ "$prompt" = '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Reply with exactly OK."}]}}' ] || exit 24
printf '%s\n%s\n' "$HOME" "$CLAUDE_CONFIG_DIR" > "$CLAUDE_AUTH_TEST_CAPTURE"
printf '%s\n' '{"type":"result","is_error":false,"terminal_reason":"completed"}'
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	verifier, err := newPinnedBareVerifier(script)
	if err != nil {
		t.Fatal(err)
	}
	verifier.environment = append(verifier.environment, "CLAUDE_AUTH_TEST_CAPTURE="+capture)
	verifier.environment = append(verifier.environment, "CLAUDE_AUTH_TEST_ORIGINAL_HOME="+originalHome)
	if err := verifier.Verify(context.Background(), []byte("sk-ant-exact-child")); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	captured, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read verification probe: %v", err)
	}
	paths := strings.Fields(string(captured))
	if len(paths) != 2 {
		t.Fatalf("verification isolation paths = %q", captured)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("verification isolation path was not cleaned up: %q, err=%v", path, err)
		}
	}
}

func TestPinnedBareVerifierClassifiesOnlyExplicitAuthenticationFailureAsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	for _, tc := range []struct {
		name   string
		output string
		exit   int
		want   error
	}{
		{name: "explicit auth failure", output: `{"type":"assistant","error":"authentication_failed"}
{"type":"result","is_error":true,"terminal_reason":"completed"}`, want: authflow.ErrProviderRejected},
		{name: "successful provider result", output: `{"type":"result","is_error":false,"terminal_reason":"completed"}`, want: nil},
		{name: "transport exit remains unknown", output: ``, exit: 9, want: authflow.ErrAuthorizationUnconfirmed},
		{name: "unclassified provider error remains unknown", output: `{"type":"result","is_error":true,"terminal_reason":"completed"}`, want: authflow.ErrAuthorizationUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			script := filepath.Join(directory, "claude")
			body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s'\nexit %d\n", tc.output, tc.exit)
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			verifier, err := newPinnedBareVerifier(script)
			if err != nil {
				t.Fatal(err)
			}
			err = verifier.Verify(context.Background(), []byte("sk-ant-test"))
			if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
				t.Fatalf("Verify() error = %v, want %v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "authentication_failed") {
				t.Fatalf("Verify() leaked provider detail: %v", err)
			}
		})
	}
}

func TestClaudeExecutableMustNotBeWritableByGroupOrOtherUsers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission assertion")
	}
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := newPinnedBareVerifier(path); !errors.Is(err, ErrClaudeCredential) {
		t.Fatalf("newPinnedBareVerifier() error = %v, want unsafe executable rejection", err)
	}
	if _, err := BuildCommandSpec(path, nil, t.TempDir(), bytes.NewReader(make([]byte, 16))); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("BuildCommandSpec() error = %v, want unsafe executable rejection", err)
	}
}

func containsArgument(arguments []string, want string) bool {
	for _, argument := range arguments {
		if argument == want {
			return true
		}
	}
	return false
}

type fakeAPIKeyVerifier struct {
	err   error
	calls int
	keys  [][]byte
}

func (verifier *fakeAPIKeyVerifier) Verify(_ context.Context, key []byte) error {
	verifier.calls++
	verifier.keys = append(verifier.keys, append([]byte(nil), key...))
	return verifier.err
}
