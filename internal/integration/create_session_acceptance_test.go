package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	workdirvalidator "bria/internal/workdir"
)

const helperProcessEnvironment = "BRIA_ACCEPTANCE_HELPER_PROCESS"

const helperProcessFailBeforeReady = "fail-before-ready"

// TestAcceptanceProviderChild is a real child-process adapter used only by the
// black-box acceptance test. The parent executes this test binary directly,
// without a shell. The child rereads the durable store before announcing
// readiness, records its observed workdir and binding, then stays alive until
// the parent aborts it.
func TestAcceptanceProviderChild(t *testing.T) {
	helperMode := os.Getenv(helperProcessEnvironment)
	if helperMode == helperProcessFailBeforeReady {
		childExit("intentional provider failure before readiness")
	}
	if helperMode != "1" {
		t.Skip("helper process entrypoint")
	}

	store, err := storage.OpenSessionStore(os.Getenv("BRIA_ACCEPTANCE_STORE"))
	if err != nil {
		childExit("open durable store before readiness: %v", err)
	}
	intentID := domain.IntentID(os.Getenv("BRIA_ACCEPTANCE_INTENT"))
	session, ok, err := store.GetByIntent(context.Background(), intentID)
	if err != nil {
		childExit("read durable session before readiness: %v", err)
	}
	if !ok {
		childExit("durable session for intent %q is missing before readiness", intentID)
	}
	if session.Status() != domain.SessionStarting {
		childExit("durable status before readiness = %q, want %q", session.Status(), domain.SessionStarting)
	}
	if _, bound := session.Binding(); bound {
		childExit("starting session unexpectedly has a provider binding")
	}

	provider := domain.Provider(os.Getenv("BRIA_PROVIDER"))
	logicalSessionID := domain.SessionID(os.Getenv("BRIA_SESSION_ID"))
	if provider != session.Provider() || logicalSessionID != session.ID() {
		childExit(
			"runtime identity = (%q, %q), durable identity = (%q, %q)",
			provider,
			logicalSessionID,
			session.Provider(),
			session.ID(),
		)
	}
	workdir, err := os.Getwd()
	if err != nil {
		childExit("read child workdir: %v", err)
	}
	providerSessionID := fmt.Sprintf("provider-%s-for-%s", provider, logicalSessionID)
	receiptPath := os.Getenv("BRIA_ACCEPTANCE_RECEIPT")
	startReceipt := providerChildReceipt{
		Kind:              "start",
		Provider:          provider,
		LogicalSessionID:  logicalSessionID,
		ProviderSessionID: providerSessionID,
		Workdir:           workdir,
		StatusBeforeReady: session.Status(),
	}
	if err := appendReceipt(receiptPath, startReceipt); err != nil {
		childExit("record child start: %v", err)
	}

	ready := struct {
		Protocol          int                                `json:"protocol"`
		Type              string                             `json:"type"`
		ProviderSessionID string                             `json:"provider_session_id"`
		Readiness         string                             `json:"readiness"`
		Authentication    sessionruntime.AuthenticationState `json:"authentication"`
	}{
		Protocol:          sessionruntime.ProtocolVersion,
		Type:              "ready",
		ProviderSessionID: providerSessionID,
		Readiness:         sessionruntime.ReadinessProtocol,
		Authentication:    sessionruntime.AuthenticationUnknown,
	}
	if err := json.NewEncoder(os.Stdout).Encode(ready); err != nil {
		childExit("write readiness handshake: %v", err)
	}

	time.Sleep(25 * time.Millisecond)
	if err := appendReceipt(receiptPath, providerChildReceipt{
		Kind:              "alive",
		Provider:          provider,
		LogicalSessionID:  logicalSessionID,
		ProviderSessionID: providerSessionID,
		Workdir:           workdir,
	}); err != nil {
		childExit("record live child receipt: %v", err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if err := decodeStrictClose(scanner.Bytes()); err != nil {
			childExit("read close command: %v", err)
		}
		if err := appendReceipt(receiptPath, providerChildReceipt{
			Kind:              "close",
			Provider:          provider,
			LogicalSessionID:  logicalSessionID,
			ProviderSessionID: providerSessionID,
			Workdir:           workdir,
		}); err != nil {
			childExit("record close command: %v", err)
		}
		return
	}
	if err := scanner.Err(); err != nil {
		childExit("scan close command: %v", err)
	}
	childExit("stdin closed before close command")
}

func TestConfirmedLocalSessionCreationAcrossAppStorageAndRuntime(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}

	providers := []domain.Provider{domain.ProviderCodex, domain.ProviderClaude}
	for _, provider := range providers {
		provider := provider
		t.Run(string(provider), func(t *testing.T) {
			root := t.TempDir()
			workdir := filepath.Join(root, "confirmed-workdir")
			if err := os.Mkdir(workdir, 0o700); err != nil {
				t.Fatalf("create confirmed workdir: %v", err)
			}
			storePath := filepath.Join(root, "sessions.json")
			receiptPath := filepath.Join(root, "child-receipts.jsonl")
			intent := app.ConfirmedSessionIntent{
				IntentID:   domain.IntentID("intent-" + string(provider)),
				ComputerID: "local-computer",
				Provider:   provider,
				Workdir:    workdir,
			}

			store, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("open session store: %v", err)
			}
			starter, err := sessionruntime.NewStarter(
				map[domain.Provider]sessionruntime.CommandSpec{
					provider: {
						Path: testBinary,
						Args: []string{"-test.run=^TestAcceptanceProviderChild$"},
						Env: []string{
							helperProcessEnvironment + "=1",
							"BRIA_ACCEPTANCE_STORE=" + storePath,
							"BRIA_ACCEPTANCE_INTENT=" + string(intent.IntentID),
							"BRIA_ACCEPTANCE_RECEIPT=" + receiptPath,
						},
					},
				},
				sessionruntime.Options{HandshakeTimeout: 2 * time.Second},
			)
			if err != nil {
				t.Fatalf("create process starter: %v", err)
			}
			ids := &sequentialSessionIDs{prefix: "logical-" + string(provider)}
			creator, err := app.NewSessionCreator(
				intent.ComputerID,
				workdirvalidator.ExistingDirectory{},
				ids,
				store,
				starter,
			)
			if err != nil {
				t.Fatalf("create session application: %v", err)
			}

			result, err := creator.Create(context.Background(), intent)
			if err != nil {
				t.Fatalf("create confirmed session: %v", err)
			}
			if result.StartError != nil {
				t.Fatalf("start confirmed session: %v", result.StartError)
			}
			if result.Replayed {
				t.Fatal("first creation was reported as replayed")
			}
			if got, want := result.Session.Status(), domain.SessionReady; got != want {
				t.Fatalf("created status = %q, want %q", got, want)
			}
			binding, ok := result.Session.Binding()
			if !ok {
				t.Fatal("ready session has no provider binding")
			}
			request := app.StartSessionRequest{
				SessionID:  result.Session.ID(),
				ComputerID: result.Session.ComputerID(),
				Provider:   result.Session.Provider(),
				Workdir:    result.Session.Workdir(),
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := starter.Abort(ctx, request, binding); err != nil {
					t.Errorf("abort helper child: %v", err)
				}
			})

			startReceipt := waitForReceipt(t, receiptPath, "start", 2*time.Second)
			if got, want := startReceipt.Provider, provider; got != want {
				t.Errorf("child provider = %q, want %q", got, want)
			}
			if got, want := startReceipt.LogicalSessionID, result.Session.ID(); got != want {
				t.Errorf("child logical session = %q, want %q", got, want)
			}
			if got, want := startReceipt.ProviderSessionID, binding.SessionID; got != want {
				t.Errorf("child provider session = %q, binding %q", got, want)
			}
			if got, want := startReceipt.StatusBeforeReady, domain.SessionStarting; got != want {
				t.Errorf("durable status observed by child = %q, want %q", got, want)
			}
			resolvedWorkdir, err := filepath.EvalSymlinks(workdir)
			if err != nil {
				t.Fatalf("resolve expected child workdir: %v", err)
			}
			if got := startReceipt.Workdir; got != resolvedWorkdir {
				t.Errorf("child workdir = %q, want exact OS path %q", got, resolvedWorkdir)
			}
			aliveReceipt := waitForReceipt(t, receiptPath, "alive", 2*time.Second)
			if got, want := aliveReceipt.ProviderSessionID, binding.SessionID; got != want {
				t.Errorf("live child provider session = %q, binding %q", got, want)
			}

			reopened, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("reopen session store from disk: %v", err)
			}
			persisted, ok, err := reopened.GetByIntent(context.Background(), intent.IntentID)
			if err != nil {
				t.Fatalf("read ready session from reopened store: %v", err)
			}
			if !ok {
				t.Fatal("reopened store has no session for confirmed intent")
			}
			assertReadySession(t, persisted, intent, result.Session.ID(), binding)

			startsBeforeReplay := countReceipts(t, receiptPath, "start")
			replayed, err := creator.Create(context.Background(), intent)
			if err != nil {
				t.Fatalf("replay confirmed intent: %v", err)
			}
			if !replayed.Replayed {
				t.Fatal("same IntentID was not reported as replayed")
			}
			if !replayed.Session.Equal(persisted) {
				t.Fatalf("replayed session = %#v, want persisted %#v", replayed.Session.Snapshot(), persisted.Snapshot())
			}
			time.Sleep(100 * time.Millisecond)
			if got := countReceipts(t, receiptPath, "start"); got != startsBeforeReplay {
				t.Fatalf("child starts after replay = %d, want unchanged %d", got, startsBeforeReplay)
			}

			conflicting := intent
			conflicting.Provider = otherProvider(provider)
			if _, err := creator.Create(context.Background(), conflicting); !errors.Is(err, app.ErrIntentConflict) {
				t.Fatalf("provider-changing replay error = %v, want %v", err, app.ErrIntentConflict)
			}
			conflicting = intent
			conflicting.Workdir = filepath.Join(root, "different-workdir")
			if _, err := creator.Create(context.Background(), conflicting); !errors.Is(err, app.ErrIntentConflict) {
				t.Fatalf("workdir-changing replay error = %v, want %v", err, app.ErrIntentConflict)
			}
			persistedAfterConflicts, ok, err := reopened.GetByIntent(context.Background(), intent.IntentID)
			if err != nil || !ok {
				t.Fatalf("read session after conflicting replay = (%v, %v)", ok, err)
			}
			if !persistedAfterConflicts.Equal(persisted) {
				t.Fatalf("conflicting replay mutated session to %#v, want %#v", persistedAfterConflicts.Snapshot(), persisted.Snapshot())
			}
			if got := countReceipts(t, receiptPath, "start"); got != 1 {
				t.Fatalf("physical child starts = %d, want 1", got)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := starter.Abort(ctx, request, binding); err != nil {
				t.Fatalf("abort helper child through protocol: %v", err)
			}
			closeReceipt := waitForReceipt(t, receiptPath, "close", 2*time.Second)
			if got, want := closeReceipt.ProviderSessionID, binding.SessionID; got != want {
				t.Errorf("closed provider session = %q, binding %q", got, want)
			}
		})
	}
}

func TestConfirmedLocalSessionStartFailurePersistsAwaitingRecovery(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}

	root := t.TempDir()
	storePath := filepath.Join(root, "sessions.json")
	workdir := filepath.Join(root, "workdir")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	intent := app.ConfirmedSessionIntent{
		IntentID:   "intent-start-failure",
		ComputerID: "local-computer",
		Provider:   domain.ProviderCodex,
		Workdir:    workdir,
	}
	store, err := storage.OpenSessionStore(storePath)
	if err != nil {
		t.Fatalf("open session store: %v", err)
	}
	starter, err := sessionruntime.NewStarter(
		map[domain.Provider]sessionruntime.CommandSpec{
			domain.ProviderCodex: {
				Path: testBinary,
				Args: []string{"-test.run=^TestAcceptanceProviderChild$"},
				Env:  []string{helperProcessEnvironment + "=" + helperProcessFailBeforeReady},
			},
		},
		sessionruntime.Options{HandshakeTimeout: time.Second},
	)
	if err != nil {
		t.Fatalf("create process starter: %v", err)
	}
	creator, err := app.NewSessionCreator(
		intent.ComputerID,
		workdirvalidator.ExistingDirectory{},
		&sequentialSessionIDs{prefix: "logical-start-failure"},
		store,
		starter,
	)
	if err != nil {
		t.Fatalf("create session application: %v", err)
	}

	result, err := creator.Create(context.Background(), intent)
	if err != nil {
		t.Fatalf("create with real process start failure: %v", err)
	}
	if result.StartError == nil || !strings.Contains(result.StartError.Error(), "provider process exited before readiness") {
		t.Fatalf("StartError = %v, want real helper failure before readiness", result.StartError)
	}
	if got, want := result.Session.Status(), domain.SessionAwaitingRecovery; got != want {
		t.Fatalf("result status = %q, want %q", got, want)
	}
	if _, ok := result.Session.Binding(); ok {
		t.Fatal("awaiting-recovery result retained a provider binding")
	}

	reopened, err := storage.OpenSessionStore(storePath)
	if err != nil {
		t.Fatalf("reopen session store from disk: %v", err)
	}
	persisted, ok, err := reopened.GetByIntent(context.Background(), intent.IntentID)
	if err != nil {
		t.Fatalf("read failed session from reopened store: %v", err)
	}
	if !ok {
		t.Fatal("reopened store has no failed session")
	}
	if !persisted.Equal(result.Session) {
		t.Fatalf("reopened session = %#v, want %#v", persisted.Snapshot(), result.Session.Snapshot())
	}
	if got, want := persisted.Status(), domain.SessionAwaitingRecovery; got != want {
		t.Fatalf("persisted status = %q, want %q", got, want)
	}
	if _, ok := persisted.Binding(); ok {
		t.Fatal("persisted awaiting-recovery session has a provider binding")
	}
}

type sequentialSessionIDs struct {
	mu     sync.Mutex
	prefix string
	next   int
}

func (source *sequentialSessionIDs) NewSessionID(context.Context) (domain.SessionID, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.next++
	return domain.SessionID(fmt.Sprintf("%s-%d", source.prefix, source.next)), nil
}

type providerChildReceipt struct {
	Kind              string               `json:"kind"`
	Provider          domain.Provider      `json:"provider"`
	LogicalSessionID  domain.SessionID     `json:"logical_session_id"`
	ProviderSessionID string               `json:"provider_session_id"`
	StartMode         app.SessionStartMode `json:"start_mode,omitempty"`
	Generation        uint64               `json:"generation,omitempty"`
	Workdir           string               `json:"workdir"`
	StatusBeforeReady domain.SessionStatus `json:"status_before_ready,omitempty"`
}

func appendReceipt(path string, receipt providerChildReceipt) error {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func childExit(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func decodeStrictClose(line []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("close command must be a JSON object")
	}
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("close command has an invalid field name")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return errors.New("close command has a duplicate or non-string field")
		}
		seen[key] = true
		switch key {
		case "protocol":
			var protocol int
			if err := decoder.Decode(&protocol); err != nil || protocol != sessionruntime.ProtocolVersion {
				return errors.New("close command has an invalid protocol")
			}
		case "type":
			var messageType string
			if err := decoder.Decode(&messageType); err != nil || messageType != "close" {
				return errors.New("close command has an invalid type")
			}
		default:
			return fmt.Errorf("close command has unknown field %q", key)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || !seen["protocol"] || !seen["type"] {
		return errors.New("close command is incomplete")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("close command has trailing data")
	}
	return nil
}

func waitForReceipt(t *testing.T, path string, kind string, timeout time.Duration) providerChildReceipt {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, receipt := range readReceipts(t, path) {
			if receipt.Kind == kind {
				return receipt
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("receipt %q did not appear within %s", kind, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countReceipts(t *testing.T, path string, kind string) int {
	t.Helper()
	count := 0
	for _, receipt := range readReceipts(t, path) {
		if receipt.Kind == kind {
			count++
		}
	}
	return count
}

func readReceipts(t *testing.T, path string) []providerChildReceipt {
	t.Helper()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("open child receipts: %v", err)
	}
	defer file.Close()

	var receipts []providerChildReceipt
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var receipt providerChildReceipt
		if err := json.Unmarshal(scanner.Bytes(), &receipt); err != nil {
			t.Fatalf("decode child receipt %q: %v", scanner.Text(), err)
		}
		receipts = append(receipts, receipt)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan child receipts: %v", err)
	}
	return receipts
}

func assertReadySession(
	t *testing.T,
	session domain.Session,
	intent app.ConfirmedSessionIntent,
	wantID domain.SessionID,
	wantBinding domain.ProviderBinding,
) {
	t.Helper()
	if got, want := session.ID(), wantID; got != want {
		t.Errorf("session id = %q, want %q", got, want)
	}
	if got, want := session.IntentID(), intent.IntentID; got != want {
		t.Errorf("intent id = %q, want %q", got, want)
	}
	if got, want := session.ComputerID(), intent.ComputerID; got != want {
		t.Errorf("computer id = %q, want %q", got, want)
	}
	if got, want := session.Provider(), intent.Provider; got != want {
		t.Errorf("provider = %q, want %q", got, want)
	}
	if got, want := session.Workdir(), intent.Workdir; got != want {
		t.Errorf("workdir = %q, want %q", got, want)
	}
	if got, want := session.Status(), domain.SessionReady; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	gotBinding, ok := session.Binding()
	if !ok {
		t.Fatal("persisted ready session has no binding")
	}
	if gotBinding != wantBinding {
		t.Errorf("binding = %#v, want %#v", gotBinding, wantBinding)
	}
}

func otherProvider(provider domain.Provider) domain.Provider {
	if provider == domain.ProviderCodex {
		return domain.ProviderClaude
	}
	return domain.ProviderCodex
}
