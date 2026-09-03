package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	workdirvalidator "bria/internal/workdir"
)

const exactResumeHelperEnvironment = "BRIA_ACCEPTANCE_EXACT_RESUME_HELPER"

const exactResumeHelperFailBeforeReady = "fail-before-ready"

const exactResumeAcceptanceTimeout = 10 * time.Second

// TestAcceptanceExactResumeProviderChild is a physical adapter boundary for
// crash recovery. It refuses to announce readiness unless the durable prior
// binding and every reserved resume environment value identify the same
// provider context and the next process generation.
func TestAcceptanceExactResumeProviderChild(t *testing.T) {
	helperMode := os.Getenv(exactResumeHelperEnvironment)
	if helperMode == "" {
		t.Skip("helper process entrypoint")
	}
	if helperMode != "1" && helperMode != exactResumeHelperFailBeforeReady {
		exactResumeChildExit("unsupported exact resume helper mode %q", helperMode)
	}

	store, err := storage.OpenSessionStore(os.Getenv("BRIA_ACCEPTANCE_STORE"))
	if err != nil {
		exactResumeChildExit("open durable store before resume readiness: %v", err)
	}
	intentID := domain.IntentID(os.Getenv("BRIA_ACCEPTANCE_INTENT"))
	persisted, ok, err := store.GetByIntent(context.Background(), intentID)
	if err != nil || !ok {
		exactResumeChildExit("read durable session before resume readiness: found=%v err=%v", ok, err)
	}
	prior, ok := persisted.Binding()
	if !ok {
		exactResumeChildExit("durable resume session has no prior provider binding")
	}
	expectedStatus := domain.SessionStatus(os.Getenv("BRIA_ACCEPTANCE_EXPECTED_STATUS"))
	if expectedStatus == "" {
		expectedStatus = domain.SessionReady
	}
	if persisted.Status() != expectedStatus {
		exactResumeChildExit("durable status before exact resume = %q, want %q", persisted.Status(), expectedStatus)
	}
	if got := os.Getenv(sessionruntime.EnvironmentStartMode); got != string(app.SessionStartResume) {
		exactResumeChildExit("start mode = %q, want %q", got, app.SessionStartResume)
	}
	if got := os.Getenv(sessionruntime.EnvironmentProviderSession); got != prior.SessionID {
		exactResumeChildExit("provider session env = %q, want durable %q", got, prior.SessionID)
	}
	generation, err := strconv.ParseUint(os.Getenv(sessionruntime.EnvironmentGeneration), 10, 64)
	if err != nil || generation != prior.Generation+1 {
		exactResumeChildExit("generation env = %q, want %d", os.Getenv(sessionruntime.EnvironmentGeneration), prior.Generation+1)
	}
	provider := domain.Provider(os.Getenv("BRIA_PROVIDER"))
	logicalSessionID := domain.SessionID(os.Getenv("BRIA_SESSION_ID"))
	if provider != persisted.Provider() || logicalSessionID != persisted.ID() {
		exactResumeChildExit("resume runtime identity = (%q, %q), durable identity = (%q, %q)", provider, logicalSessionID, persisted.Provider(), persisted.ID())
	}
	workdir, err := os.Getwd()
	if err != nil {
		exactResumeChildExit("read resume child workdir: %v", err)
	}
	if workdir != persisted.Workdir() {
		exactResumeChildExit("resume workdir = %q, durable workdir %q", workdir, persisted.Workdir())
	}
	receiptKind := "resume"
	if helperMode == exactResumeHelperFailBeforeReady {
		receiptKind = "resume-attempt"
	}
	if err := appendReceipt(os.Getenv("BRIA_ACCEPTANCE_RECEIPT"), providerChildReceipt{
		Kind: receiptKind, Provider: provider, LogicalSessionID: logicalSessionID,
		ProviderSessionID: prior.SessionID, StartMode: app.SessionStartResume,
		Generation: generation, Workdir: workdir, StatusBeforeReady: persisted.Status(),
	}); err != nil {
		exactResumeChildExit("record exact resume receipt: %v", err)
	}
	if helperMode == exactResumeHelperFailBeforeReady {
		exactResumeChildExit("intentional exact resume failure before readiness")
	}

	ready := struct {
		Protocol          int                                `json:"protocol"`
		Type              string                             `json:"type"`
		ProviderSessionID string                             `json:"provider_session_id"`
		Readiness         string                             `json:"readiness"`
		Authentication    sessionruntime.AuthenticationState `json:"authentication"`
	}{
		Protocol: sessionruntime.ProtocolVersion, Type: "ready",
		ProviderSessionID: prior.SessionID, Readiness: sessionruntime.ReadinessProtocol,
		Authentication: sessionruntime.AuthenticationUnknown,
	}
	if err := json.NewEncoder(os.Stdout).Encode(ready); err != nil {
		exactResumeChildExit("write exact resume readiness: %v", err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if err := decodeStrictClose(scanner.Bytes()); err != nil {
			exactResumeChildExit("read exact resume close command: %v", err)
		}
		if err := appendReceipt(os.Getenv("BRIA_ACCEPTANCE_RECEIPT"), providerChildReceipt{
			Kind: "resume-close", Provider: provider, LogicalSessionID: logicalSessionID,
			ProviderSessionID: prior.SessionID, StartMode: app.SessionStartResume,
			Generation: generation, Workdir: workdir,
		}); err != nil {
			exactResumeChildExit("record exact resume close: %v", err)
		}
		return
	}
	if err := scanner.Err(); err != nil {
		exactResumeChildExit("scan exact resume close command: %v", err)
	}
	exactResumeChildExit("stdin closed before exact resume close command")
}

func exactResumeChildExit(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	_ = appendReceipt(os.Getenv("BRIA_ACCEPTANCE_RECEIPT"), providerChildReceipt{
		Kind: "helper-error", ProviderSessionID: message,
	})
	childExit("%s", message)
}

func TestArchivedSessionExactResumeIsAtomicAndDurable(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}

	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		provider := provider
		t.Run(string(provider), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			workdir := filepath.Join(root, "workdir")
			if err := os.Mkdir(workdir, 0o700); err != nil {
				t.Fatalf("create workdir: %v", err)
			}
			workdir, err = filepath.EvalSymlinks(workdir)
			if err != nil {
				t.Fatalf("resolve physical workdir: %v", err)
			}
			storePath := filepath.Join(root, "sessions.json")
			intentID := domain.IntentID("archive-intent-" + string(provider))
			createdAt := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
			starting, err := domain.NewStartingSessionAt(
				"archived-logical-"+domain.SessionID(provider), intentID, "computer-1", provider,
				workdir, createdAt, domain.SessionLifetimeNever,
			)
			if err != nil {
				t.Fatalf("create starting session: %v", err)
			}
			prior := domain.ProviderBinding{Provider: provider, SessionID: "original-provider-" + string(provider), Generation: 4}
			ready, err := starting.ReadyAt(prior, createdAt.Add(time.Minute))
			if err != nil {
				t.Fatalf("mark ready: %v", err)
			}
			closing, err := ready.BeginClose(createdAt.Add(2 * time.Minute))
			if err != nil {
				t.Fatalf("begin close: %v", err)
			}
			archived, err := closing.Archive(createdAt.Add(3 * time.Minute))
			if err != nil {
				t.Fatalf("archive: %v", err)
			}
			store, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("open archive store: %v", err)
			}
			if _, inserted, err := store.PutStartingIfAbsent(ctx, starting); err != nil || !inserted {
				t.Fatalf("persist starting = (inserted=%v, err=%v)", inserted, err)
			}
			for _, transition := range []struct{ from, to domain.Session }{{starting, ready}, {ready, closing}, {closing, archived}} {
				if err := store.Replace(ctx, transition.from, transition.to); err != nil {
					t.Fatalf("persist lifecycle %q -> %q: %v", transition.from.Status(), transition.to.Status(), err)
				}
			}

			beforeFailure, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatalf("read archive before failed resume: %v", err)
			}
			failureReceiptPath := filepath.Join(root, "failed-resume.jsonl")
			failingStarter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				provider: exactResumeCommand(testBinary, storePath, intentID, failureReceiptPath, exactResumeHelperFailBeforeReady, domain.SessionArchived),
			}, sessionruntime.Options{HandshakeTimeout: exactResumeAcceptanceTimeout})
			if err != nil {
				t.Fatalf("create failing resume starter: %v", err)
			}
			resumeAt := createdAt.Add(time.Hour)
			failingResumer, err := app.NewArchivedSessionResumer(store, failingStarter, domain.SessionLifetime6Hours, func() time.Time { return resumeAt })
			if err != nil {
				t.Fatalf("create failing archived resumer: %v", err)
			}
			if _, err := failingResumer.Resume(ctx, archived.ID()); err == nil {
				t.Fatal("physical provider failure unexpectedly resumed archive")
			}
			attempt := waitForReceipt(t, failureReceiptPath, "resume-attempt", exactResumeAcceptanceTimeout)
			if attempt.ProviderSessionID != prior.SessionID || attempt.Generation != prior.Generation+1 ||
				attempt.StatusBeforeReady != domain.SessionArchived {
				t.Fatalf("failed physical resume attempt = %#v", attempt)
			}
			afterFailure, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatalf("read archive after failed resume: %v", err)
			}
			if string(afterFailure) != string(beforeFailure) {
				t.Fatal("failed exact resume changed durable archive bytes")
			}
			afterFailureStore, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("reopen archive after failed resume: %v", err)
			}
			unchanged, err := afterFailureStore.Load(ctx, archived.ID())
			if err != nil || !unchanged.Equal(archived) {
				t.Fatalf("failed resume archive = (%#v, %v), want unchanged %#v", unchanged.Snapshot(), err, archived.Snapshot())
			}

			successReceiptPath := filepath.Join(root, "successful-resume.jsonl")
			successStarter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				provider: exactResumeCommand(testBinary, storePath, intentID, successReceiptPath, "1", domain.SessionArchived),
			}, sessionruntime.Options{HandshakeTimeout: exactResumeAcceptanceTimeout})
			if err != nil {
				t.Fatalf("create successful resume starter: %v", err)
			}
			resumer, err := app.NewArchivedSessionResumer(afterFailureStore, successStarter, domain.SessionLifetime6Hours, func() time.Time { return resumeAt })
			if err != nil {
				t.Fatalf("create archived resumer: %v", err)
			}
			resumed, err := resumer.Resume(ctx, archived.ID())
			if err != nil {
				t.Fatalf("resume archived provider session: %v", err)
			}
			binding, ok := resumed.Binding()
			wantBinding := prior
			wantBinding.Generation++
			if !ok || binding != wantBinding || resumed.Status() != domain.SessionReady {
				t.Fatalf("resumed archive binding/state = (%#v, %v, %q), want (%#v, true, ready)", binding, ok, resumed.Status(), wantBinding)
			}
			if got, ok := resumed.LastResumedAt(); !ok || !got.Equal(resumeAt) {
				t.Fatalf("last resumed at = (%s, %v), want %s", got, ok, resumeAt)
			}
			if got, ok := resumed.Deadline(); !ok || !got.Equal(resumeAt.Add(6*time.Hour)) {
				t.Fatalf("resumed deadline = (%s, %v), want %s", got, ok, resumeAt.Add(6*time.Hour))
			}
			receipt := waitForReceipt(t, successReceiptPath, "resume", exactResumeAcceptanceTimeout)
			if receipt.ProviderSessionID != prior.SessionID || receipt.Generation != prior.Generation+1 ||
				receipt.StatusBeforeReady != domain.SessionArchived {
				t.Fatalf("successful physical resume receipt = %#v", receipt)
			}
			persistedStore, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("reopen successful archive resume: %v", err)
			}
			persisted, err := persistedStore.Load(ctx, archived.ID())
			if err != nil || !persisted.Equal(resumed) {
				t.Fatalf("persisted resumed archive = (%#v, %v), want %#v", persisted.Snapshot(), err, resumed.Snapshot())
			}

			request := app.StartSessionRequest{
				SessionID: resumed.ID(), ComputerID: resumed.ComputerID(), Provider: resumed.Provider(),
				Workdir: resumed.Workdir(), Mode: app.SessionStartResume, PriorBinding: &prior,
			}
			closeCtx, cancelClose := context.WithTimeout(ctx, exactResumeAcceptanceTimeout)
			if err := successStarter.Abort(closeCtx, request, binding); err != nil {
				cancelClose()
				t.Fatalf("stop resumed archived adapter: %v", err)
			}
			cancelClose()
			waitForReceipt(t, successReceiptPath, "resume-close", exactResumeAcceptanceTimeout)
		})
	}
}

func exactResumeCommand(
	testBinary string,
	storePath string,
	intentID domain.IntentID,
	receiptPath string,
	helperMode string,
	expectedStatus domain.SessionStatus,
) sessionruntime.CommandSpec {
	return sessionruntime.CommandSpec{
		Path: testBinary,
		Args: []string{"-test.run=^TestAcceptanceExactResumeProviderChild$"},
		Env: []string{
			exactResumeHelperEnvironment + "=" + helperMode,
			"BRIA_ACCEPTANCE_STORE=" + storePath,
			"BRIA_ACCEPTANCE_INTENT=" + string(intentID),
			"BRIA_ACCEPTANCE_RECEIPT=" + receiptPath,
			"BRIA_ACCEPTANCE_EXPECTED_STATUS=" + string(expectedStatus),
		},
	}
}

func TestExactResumeAcrossRecoveryStorageAndRuntime(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}

	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		provider := provider
		t.Run(string(provider), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			workdir := filepath.Join(root, "workdir")
			if err := os.Mkdir(workdir, 0o700); err != nil {
				t.Fatalf("create workdir: %v", err)
			}
			workdir, err = filepath.EvalSymlinks(workdir)
			if err != nil {
				t.Fatalf("resolve physical workdir: %v", err)
			}
			storePath := filepath.Join(root, "sessions.json")
			initialReceiptPath := filepath.Join(root, "initial.jsonl")
			resumeReceiptPath := filepath.Join(root, "resume.jsonl")
			intent := app.ConfirmedSessionIntent{
				IntentID:   "resume-intent-" + domain.IntentID(provider),
				ComputerID: "computer-1", Provider: provider, Workdir: workdir,
			}

			store, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("open initial store: %v", err)
			}
			initialStarter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				provider: {
					Path: testBinary, Args: []string{"-test.run=^TestAcceptanceProviderChild$"},
					Env: []string{helperProcessEnvironment + "=1", "BRIA_ACCEPTANCE_STORE=" + storePath,
						"BRIA_ACCEPTANCE_INTENT=" + string(intent.IntentID), "BRIA_ACCEPTANCE_RECEIPT=" + initialReceiptPath},
				},
			}, sessionruntime.Options{HandshakeTimeout: exactResumeAcceptanceTimeout})
			if err != nil {
				t.Fatalf("create initial starter: %v", err)
			}
			creator, err := app.NewSessionCreator(intent.ComputerID, workdirvalidator.ExistingDirectory{},
				&sequentialSessionIDs{prefix: "logical-resume-" + string(provider)}, store, initialStarter)
			if err != nil {
				t.Fatalf("create session creator: %v", err)
			}
			created, err := creator.Create(ctx, intent)
			if err != nil || created.StartError != nil {
				t.Fatalf("create initial provider session = (%#v, %v)", created, err)
			}
			prior, ok := created.Session.Binding()
			if !ok {
				t.Fatal("initial ready session has no binding")
			}
			initialRequest := app.StartSessionRequest{
				SessionID: created.Session.ID(), ComputerID: intent.ComputerID,
				Provider: provider, Workdir: workdir, Mode: app.SessionStartNew,
			}
			abortCtx, cancelAbort := context.WithTimeout(ctx, exactResumeAcceptanceTimeout)
			if err := initialStarter.Abort(abortCtx, initialRequest, prior); err != nil {
				cancelAbort()
				t.Fatalf("physically stop initial adapter: %v", err)
			}
			cancelAbort()
			waitForReceipt(t, initialReceiptPath, "close", exactResumeAcceptanceTimeout)

			reopened, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("reopen store for recovery: %v", err)
			}
			resumeStarter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
				provider: {
					Path: testBinary, Args: []string{"-test.run=^TestAcceptanceExactResumeProviderChild$"},
					Env: []string{exactResumeHelperEnvironment + "=1", "BRIA_ACCEPTANCE_STORE=" + storePath,
						"BRIA_ACCEPTANCE_INTENT=" + string(intent.IntentID), "BRIA_ACCEPTANCE_RECEIPT=" + resumeReceiptPath},
				},
			}, sessionruntime.Options{HandshakeTimeout: exactResumeAcceptanceTimeout})
			if err != nil {
				t.Fatalf("create exact resume starter: %v", err)
			}
			result, err := app.RecoverPersistedSessionsForComputer(ctx, intent.ComputerID, reopened, resumeStarter)
			if err != nil {
				t.Fatalf("recover persisted provider session: %v", err)
			}
			if result.Recovered != 1 || result.Awaiting != 0 || result.SkippedRemote != 0 || len(result.Sessions) != 1 {
				receiptBytes, receiptErr := os.ReadFile(resumeReceiptPath)
				persisted, persistedErr := reopened.Load(ctx, created.Session.ID())
				t.Fatalf(
					"recovery result = %#v, want one exact recovered session; helper receipts=%q receipt_err=%v persisted=%#v persisted_err=%v",
					result, receiptBytes, receiptErr, persisted.Snapshot(), persistedErr,
				)
			}
			resumed := result.Sessions[0]
			resumedBinding, ok := resumed.Binding()
			if !ok {
				t.Fatal("recovered ready session has no binding")
			}
			wantBinding := prior
			wantBinding.Generation++
			if resumedBinding != wantBinding {
				t.Fatalf("resumed binding = %#v, want same provider session with next generation %#v", resumedBinding, wantBinding)
			}
			if resumed.ID() != created.Session.ID() || resumed.IntentID() != intent.IntentID ||
				resumed.ComputerID() != intent.ComputerID || resumed.Provider() != provider ||
				resumed.Workdir() != workdir || resumed.Status() != domain.SessionReady {
				t.Fatalf("recovered logical identity/state changed: %#v", resumed.Snapshot())
			}
			receipt := waitForReceipt(t, resumeReceiptPath, "resume", exactResumeAcceptanceTimeout)
			if receipt.ProviderSessionID != prior.SessionID || receipt.StartMode != app.SessionStartResume ||
				receipt.Generation != prior.Generation+1 || receipt.StatusBeforeReady != domain.SessionReady {
				t.Fatalf("physical exact resume receipt = %#v", receipt)
			}

			physical, err := storage.OpenSessionStore(storePath)
			if err != nil {
				t.Fatalf("reopen store after recovery: %v", err)
			}
			persisted, ok, err := physical.GetByIntent(ctx, intent.IntentID)
			if err != nil || !ok || !persisted.Equal(resumed) {
				t.Fatalf("persisted recovered session = (%#v, %v, %v), want %#v", persisted.Snapshot(), ok, err, resumed.Snapshot())
			}

			resumeRequest := app.StartSessionRequest{
				SessionID: resumed.ID(), ComputerID: resumed.ComputerID(), Provider: resumed.Provider(),
				Workdir: resumed.Workdir(), Mode: app.SessionStartResume, PriorBinding: &prior,
			}
			closeCtx, cancelClose := context.WithTimeout(ctx, exactResumeAcceptanceTimeout)
			if err := resumeStarter.Abort(closeCtx, resumeRequest, resumedBinding); err != nil {
				cancelClose()
				t.Fatalf("physically stop resumed adapter: %v", err)
			}
			cancelClose()
			closeReceipt := waitForReceipt(t, resumeReceiptPath, "resume-close", exactResumeAcceptanceTimeout)
			if closeReceipt.ProviderSessionID != prior.SessionID || closeReceipt.Generation != prior.Generation+1 {
				t.Fatalf("physical close receipt = %#v", closeReceipt)
			}
		})
	}
}

func TestRecoveryFinalizesInterruptedClosingOnlyAfterExactProcessExit(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		for _, interruptedStatus := range []domain.SessionStatus{domain.SessionClosing, domain.SessionClosingAfterWork} {
			provider, interruptedStatus := provider, interruptedStatus
			t.Run(string(provider)+"/"+string(interruptedStatus), func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				workdir := filepath.Join(root, "workdir")
				if err := os.Mkdir(workdir, 0o700); err != nil {
					t.Fatalf("create workdir: %v", err)
				}
				workdir, err = filepath.EvalSymlinks(workdir)
				if err != nil {
					t.Fatalf("resolve physical workdir: %v", err)
				}
				storePath := filepath.Join(root, "sessions.json")
				receiptPath := filepath.Join(root, "closing-recovery.jsonl")
				intentID := domain.IntentID("closing-recovery-" + string(provider) + "-" + string(interruptedStatus))
				at := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
				starting, err := domain.NewStartingSessionAt(
					domain.SessionID("logical-"+string(provider)+"-"+string(interruptedStatus)), intentID,
					"computer-1", provider, workdir, at, domain.SessionLifetimeNever,
				)
				if err != nil {
					t.Fatalf("create starting session: %v", err)
				}
				prior := domain.ProviderBinding{Provider: provider, SessionID: "original-closing-" + string(provider), Generation: 7}
				ready, err := starting.ReadyAt(prior, at.Add(time.Minute))
				if err != nil {
					t.Fatalf("create ready session: %v", err)
				}
				interrupted := ready
				if interruptedStatus == domain.SessionClosingAfterWork {
					interrupted, err = ready.StartWork(at.Add(2 * time.Minute))
					if err == nil {
						interrupted, err = interrupted.CloseAfterWork(at.Add(3 * time.Minute))
					}
				} else {
					interrupted, err = ready.BeginClose(at.Add(2 * time.Minute))
				}
				if err != nil || interrupted.Status() != interruptedStatus {
					t.Fatalf("build interrupted %q = (%#v, %v)", interruptedStatus, interrupted.Snapshot(), err)
				}
				store, err := storage.OpenSessionStore(storePath)
				if err != nil {
					t.Fatalf("open session store: %v", err)
				}
				if _, inserted, err := store.PutStartingIfAbsent(ctx, starting); err != nil || !inserted {
					t.Fatalf("persist starting = (%v, %v)", inserted, err)
				}
				if err := store.Replace(ctx, starting, ready); err != nil {
					t.Fatalf("persist ready: %v", err)
				}
				if err := store.Replace(ctx, ready, interrupted); err != nil {
					t.Fatalf("persist interrupted close: %v", err)
				}

				starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
					provider: exactResumeCommand(testBinary, storePath, intentID, receiptPath, "1", interruptedStatus),
				}, sessionruntime.Options{HandshakeTimeout: exactResumeAcceptanceTimeout})
				if err != nil {
					t.Fatalf("create exact recovery starter: %v", err)
				}
				result, err := app.RecoverPersistedSessionsForComputer(ctx, "computer-1", store, starter)
				if err != nil {
					t.Fatalf("recover interrupted close: %v", err)
				}
				if result.Recovered != 0 || result.Awaiting != 0 || result.FinalizedClosing != 1 || len(result.Sessions) != 0 {
					t.Fatalf("closing recovery result = %#v, want one finalized close and no live session", result)
				}
				resumeReceipt := waitForReceipt(t, receiptPath, "resume", exactResumeAcceptanceTimeout)
				closeReceipt := waitForReceipt(t, receiptPath, "resume-close", exactResumeAcceptanceTimeout)
				if resumeReceipt.ProviderSessionID != prior.SessionID || closeReceipt.ProviderSessionID != prior.SessionID ||
					resumeReceipt.Generation != prior.Generation+1 || closeReceipt.Generation != prior.Generation+1 ||
					resumeReceipt.StatusBeforeReady != interruptedStatus {
					t.Fatalf("physical closing recovery receipts = resume %#v close %#v, prior %#v", resumeReceipt, closeReceipt, prior)
				}
				archived := loadSession(t, storePath, interrupted.ID())
				binding, ok := archived.Binding()
				if !ok || archived.Status() != domain.SessionArchived || binding.SessionID != prior.SessionID ||
					binding.Generation != prior.Generation+1 {
					t.Fatalf("durable finalized close = %#v, want archived exact provider generation", archived.Snapshot())
				}
			})
		}
	}
}

func TestRecoveryDoesNotInventAnInFlightTurnAfterProcessExit(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve integration test executable: %v", err)
	}
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		for _, interruptedStatus := range []domain.SessionStatus{domain.SessionRunning, domain.SessionStopping} {
			provider, interruptedStatus := provider, interruptedStatus
			t.Run(string(provider)+"/"+string(interruptedStatus), func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				workdir := filepath.Join(root, "workdir")
				if err := os.Mkdir(workdir, 0o700); err != nil {
					t.Fatalf("create workdir: %v", err)
				}
				workdir, err = filepath.EvalSymlinks(workdir)
				if err != nil {
					t.Fatalf("resolve physical workdir: %v", err)
				}
				storePath := filepath.Join(root, "sessions.json")
				receiptPath := filepath.Join(root, "turn-recovery.jsonl")
				intentID := domain.IntentID("turn-recovery-" + string(provider) + "-" + string(interruptedStatus))
				at := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
				starting, err := domain.NewStartingSessionAt(
					domain.SessionID("logical-"+string(provider)+"-"+string(interruptedStatus)), intentID,
					"computer-1", provider, workdir, at, domain.SessionLifetimeNever,
				)
				if err != nil {
					t.Fatalf("create starting session: %v", err)
				}
				prior := domain.ProviderBinding{Provider: provider, SessionID: "original-turn-" + string(provider), Generation: 3}
				ready, err := starting.ReadyAt(prior, at.Add(time.Minute))
				if err != nil {
					t.Fatalf("create ready session: %v", err)
				}
				interrupted, err := ready.StartWork(at.Add(2 * time.Minute))
				if err == nil && interruptedStatus == domain.SessionStopping {
					interrupted, err = interrupted.BeginStop(at.Add(3 * time.Minute))
				}
				if err != nil || interrupted.Status() != interruptedStatus {
					t.Fatalf("build interrupted %q = (%#v, %v)", interruptedStatus, interrupted.Snapshot(), err)
				}
				store, err := storage.OpenSessionStore(storePath)
				if err != nil {
					t.Fatalf("open session store: %v", err)
				}
				if _, inserted, err := store.PutStartingIfAbsent(ctx, starting); err != nil || !inserted {
					t.Fatalf("persist starting = (%v, %v)", inserted, err)
				}
				if err := store.Replace(ctx, starting, ready); err != nil {
					t.Fatalf("persist ready: %v", err)
				}
				if err := store.Replace(ctx, ready, interrupted); err != nil {
					t.Fatalf("persist interrupted turn: %v", err)
				}
				starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{
					provider: exactResumeCommand(testBinary, storePath, intentID, receiptPath, "1", interruptedStatus),
				}, sessionruntime.Options{HandshakeTimeout: exactResumeAcceptanceTimeout})
				if err != nil {
					t.Fatalf("create exact recovery starter: %v", err)
				}
				result, err := app.RecoverPersistedSessionsForComputer(ctx, "computer-1", store, starter)
				if err != nil {
					t.Fatalf("recover interrupted turn: %v", err)
				}
				if result.Recovered != 1 || result.Awaiting != 0 || result.FinalizedClosing != 0 || len(result.Sessions) != 1 {
					t.Fatalf("turn recovery result = %#v, want one ready session", result)
				}
				recovered := result.Sessions[0]
				binding, ok := recovered.Binding()
				if !ok || recovered.Status() != domain.SessionReady || binding.SessionID != prior.SessionID || binding.Generation != prior.Generation+1 {
					t.Fatalf("recovered interrupted turn = %#v, prior %#v", recovered.Snapshot(), prior)
				}
				receipt := waitForReceipt(t, receiptPath, "resume", exactResumeAcceptanceTimeout)
				if receipt.StatusBeforeReady != interruptedStatus || receipt.ProviderSessionID != prior.SessionID {
					t.Fatalf("physical interrupted-turn resume = %#v", receipt)
				}
				request := app.StartSessionRequest{
					SessionID: recovered.ID(), ComputerID: recovered.ComputerID(), Provider: recovered.Provider(),
					Workdir: recovered.Workdir(), Mode: app.SessionStartResume, PriorBinding: &prior,
				}
				if err := starter.Abort(ctx, request, binding); err != nil {
					t.Fatalf("close recovered adapter: %v", err)
				}
			})
		}
	}
}
