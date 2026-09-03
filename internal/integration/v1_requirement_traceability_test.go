package integration_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

type v1EvidenceLevel string

const (
	v1Synthetic v1EvidenceLevel = "synthetic"
	v1Component v1EvidenceLevel = "component"
	v1External  v1EvidenceLevel = "external-unverified"
)

type v1RequirementTrace struct {
	Area        string
	Source      string
	Requirement string
	Level       v1EvidenceLevel
	ProbeFile   string
	ProbeTest   string
	Limit       string
}

// v1RequirementTrace is deliberately explicit: an automated probe proves only
// its named boundary. External gates remain unverified until a real receipt is
// captured and must never become green merely because a fake passed.
var v1RequirementTraces = []v1RequirementTrace{
	{"user-flow", "docs/TESTING_AND_ACCEPTANCE.md", "owner update creates one durable ready Codex or Claude session", v1Synthetic, "internal/integration/telegram_creation_acceptance_test.go", "TestNormalizedOwnerEventCreatesOneDurableReadySession", "synthetic Telegram event and provider child"},
	{"user-flow", "docs/TESTING_AND_ACCEPTANCE.md", "single-computer create, submit, close, exact resume, submit and close", v1Synthetic, "internal/integration/single_computer_e2e_acceptance_test.go", "TestSingleComputerSyntheticTelegramCreateSubmitCloseAndExactResume", "synthetic coordinator source, sender and provider child"},
	{"user-flow", "docs/TELEGRAM_UX.md", "callbacks edit one carrier and never expose Clear", v1Synthetic, "internal/integration/telegram_callback_acceptance_test.go", "TestTelegramCallbackEditIsOneInPlaceOperation", "Bot API is simulated"},
	{"user-flow", "docs/TELEGRAM_UX.md", "reply, caption and supported media identity survive normalization", v1Synthetic, "internal/integration/telegram_input_acceptance_test.go", "TestTelegramReplyAndMediaSurviveTransportNormalization", "download and provider are simulated"},
	{"lifecycle", "docs/SESSION_LIFECYCLE.md", "every durable lifecycle state round-trips", v1Synthetic, "internal/integration/lifecycle_persistence_acceptance_test.go", "TestLifecycleStateMachineRoundTripsEveryDurableState", "does not prove every Telegram action is wired"},
	{"lifecycle", "docs/SESSION_LIFECYCLE.md", "failed archive resume leaves bytes unchanged and successful resume preserves provider identity", v1Synthetic, "internal/integration/exact_resume_acceptance_test.go", "TestArchivedSessionExactResumeIsAtomicAndDurable", "provider child implements the neutral protocol"},
	{"lifecycle", "docs/SESSION_LIFECYCLE.md", "restart recovery resumes the same provider identity on its owning computer", v1Synthetic, "internal/integration/exact_resume_acceptance_test.go", "TestExactResumeAcrossRecoveryStorageAndRuntime", "single-process local recovery only"},
	{"lifecycle", "docs/SESSION_LIFECYCLE.md", "restart during running or stopping resumes ready without inventing an in-flight turn", v1Synthetic, "internal/integration/exact_resume_acceptance_test.go", "TestRecoveryDoesNotInventAnInFlightTurnAfterProcessExit", "provider child implements the neutral protocol"},
	{"lifecycle", "docs/SESSION_LIFECYCLE.md", "restart during closing exactly resumes, confirms process exit and archives without exposing a live session", v1Synthetic, "internal/integration/exact_resume_acceptance_test.go", "TestRecoveryFinalizesInterruptedClosingOnlyAfterExactProcessExit", "provider child implements the neutral protocol"},
	{"lifecycle", "docs/SESSION_LIFECYCLE.md", "busy close waits for accepted work, rejects early process exit and then archives", v1Synthetic, "internal/integration/single_computer_e2e_acceptance_test.go", "TestBusySessionCloseWaitsForAcceptedWorkThenPhysicallyArchives", "provider child and Telegram input are synthetic"},
	{"lifecycle", "docs/SESSION_LIFECYCLE.md", "unexpected physical provider exit is supervised into the exact same provider session", v1Synthetic, "internal/integration/single_computer_e2e_acceptance_test.go", "TestUnexpectedPhysicalExitIsSupervisedIntoExactSameSession", "same-machine synthetic provider child"},
	{"lifecycle", "docs/SESSION_LIFECYCLE.md", "busy expiry waits for accepted work and idle expiry closes", v1Component, "internal/sessionexpiry/scheduler_test.go", "TestSweepClosesOnlyExpiredOpenSessionsInStableOrder", "scheduler and closer are tested without wall-clock daemon operation"},
	{"durability", "docs/RELIABILITY_AND_BACKUP.md", "input and output sequence survives restart and unknown delivery is not retried", v1Synthetic, "internal/integration/message_journal_acceptance_test.go", "TestMessageJournalSurvivesReopenWithoutReorderingOrRetryingUnknownDelivery", "local filesystem journal"},
	{"durability", "docs/TESTING_AND_ACCEPTANCE.md", "provider acceptance and Telegram delivery custody are explicit durable phases", v1Component, "internal/durableflow/flow_test.go", "TestDispatchRecordsRealAcceptanceAndIndependentCompletion", "provider and sender are fakes"},
	{"providers", "docs/TESTING_AND_ACCEPTANCE.md", "question and approval interactions are typed and double-correlated", v1Component, "internal/runtimeprotocol/protocol_test.go", "TestInteractionExchangeRequiresTurnAndInteractionCorrelation", "neutral wire codec only"},
	{"providers", "docs/SESSION_LIFECYCLE.md", "Claude exact resume uses the original provider session ID", v1Component, "cmd/bria-claude-adapter/main_test.go", "TestRunSelectsExactResumeFromRuntimeContractWithoutGeneratingID", "Claude CLI is replaced by a test process"},
	{"security", "docs/SECURITY.md", "authorization secrets are deleted on every terminal path and never serialized", v1Component, "internal/authflow/service_test.go", "TestSubmitDeletesSecretMessageOnEveryTerminalPath", "provider and Telegram delete are fakes"},
	{"security", "docs/SECURITY.md", "production Telegram HTTP rejects redirects and non-official hosts", v1Component, "internal/telegram/production_http_test.go", "TestProductionHTTPClientAllowsOnlyOfficialTLSHost", "no real Telegram request"},
	{"multi-node", "docs/ARCHITECTURE.md", "executor-initiated channel uses mutually authenticated pinned TLS", v1Component, "internal/nodelink/tls_channel_test.go", "TestExecutorInitiatesMutuallyAuthenticatedPinnedTLSChannel", "in-process loopback TLS"},
	{"multi-node", "docs/ARCHITECTURE.md", "operation replay fence survives restart", v1Component, "internal/nodelink/file_ledger_test.go", "TestFileOperationLedgerDeduplicatesAfterReopen", "local file ledger"},
	{"files-media", "docs/TESTING_AND_ACCEPTANCE.md", "local result links are strict and files are reopened safely", v1Component, "internal/files/links_test.go", "TestOpenFinalFilesVerifiesAndReopensEveryLink", "Telegram upload is separate"},
	{"files-media", "docs/TESTING_AND_ACCEPTANCE.md", "voice transcript command is bounded and shell-free", v1Component, "internal/speech/parakeet/command_test.go", "TestCommandReturnsBoundedTrimmedTranscriptWithoutShell", "Parakeet executable is a test helper"},
	{"backup", "docs/RELIABILITY_AND_BACKUP.md", "backup contains only six required state classes and excludes secrets", v1Component, "internal/backupflow/flow_test.go", "TestCreateLatestRejectsEveryForbiddenDataClass", "isolated local filesystem"},
	{"update", "docs/TESTING_AND_ACCEPTANCE.md", "signed exact-platform rollout updates executors first and can roll back", v1Component, "internal/update/update_test.go", "TestRolloutOrdersExecutorsBeforeCoordinatorAndStopsWhenAvailabilityUnknown", "node update operations are fakes"},
	{"settings", "docs/PRODUCT.md", "valid local edits apply atomically and invalid edits retain the last valid settings", v1Component, "internal/settings/settings_test.go", "TestFileStoreReloadAppliesValidLocalEditAndRetainsLastValidOnInvalidEdit", "Telegram settings share a separate controller seam"},
	{"architecture", "docs/ARCHITECTURE.md", "repository policy and registered package boundaries fail closed", v1Component, "scripts/check_repo_test.go", "TestArchitectureCheckerRejectsUnregisteredInternalProductionPackage", "static repository graph"},

	{"live-telegram", "docs/TESTING_AND_ACCEPTANCE.md", "real owner message, callback, edit, secret deletion, file upload and receipts", v1External, "", "", "requires separately authorized Telegram writes"},
	{"live-providers", "docs/TESTING_AND_ACCEPTANCE.md", "real installed Codex and Claude create, submit, interaction, stop and exact resume", v1External, "", "", "requires provider credentials and separately authorized provider sessions"},
	{"live-multi-node", "docs/TESTING_AND_ACCEPTANCE.md", "two real computers pair, disconnect, buffer, reconnect and preserve exact sessions", v1External, "", "", "requires two authorized machines and network configuration"},
	{"live-platforms", "docs/PLATFORMS_AND_DEPLOYMENT.md", "install, run, update and rollback on macOS, Linux, WSL and Docker", v1External, "", "", "requires platform environments and installation authorization"},
	{"live-discovery", "docs/SESSION_LIFECYCLE.md", "existing external Codex and Claude sessions are discovered without copying legacy state", v1External, "", "", "requires real provider stores for every supported version and platform"},
}

func TestV1RequirementTraceabilityReferencesExecutableProbes(t *testing.T) {
	repository := repositoryRoot(t)
	areas := make(map[string]map[v1EvidenceLevel]bool)
	seen := make(map[string]bool)
	for _, trace := range v1RequirementTraces {
		key := trace.Area + "\x00" + trace.Requirement
		if trace.Area == "" || trace.Source == "" || trace.Requirement == "" || trace.Limit == "" || seen[key] {
			t.Fatalf("invalid or duplicate v1 trace: %#v", trace)
		}
		seen[key] = true
		if _, err := os.Stat(filepath.Join(repository, trace.Source)); err != nil {
			t.Fatalf("requirement source %q: %v", trace.Source, err)
		}
		if areas[trace.Area] == nil {
			areas[trace.Area] = make(map[v1EvidenceLevel]bool)
		}
		areas[trace.Area][trace.Level] = true
		switch trace.Level {
		case v1Synthetic, v1Component:
			if trace.ProbeFile == "" || trace.ProbeTest == "" {
				t.Fatalf("automated trace has no executable probe: %#v", trace)
			}
			assertGoTestExists(t, repository, trace.ProbeFile, trace.ProbeTest)
		case v1External:
			if trace.ProbeFile != "" || trace.ProbeTest != "" {
				t.Fatalf("external-unverified trace falsely references an automated receipt: %#v", trace)
			}
		default:
			t.Fatalf("unsupported v1 evidence level %q", trace.Level)
		}
	}

	wantExternal := []string{"live-discovery", "live-multi-node", "live-platforms", "live-providers", "live-telegram"}
	for _, area := range wantExternal {
		if !areas[area][v1External] {
			t.Errorf("release gate %q is not explicitly preserved as external-unverified", area)
		}
	}
	var names []string
	for area := range areas {
		names = append(names, area)
	}
	sort.Strings(names)
	t.Logf("v1 traceability areas: %v", names)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve traceability source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func assertGoTestExists(t *testing.T, repository, relativePath, name string) {
	t.Helper()
	path := filepath.Join(repository, relativePath)
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse trace probe %q: %v", relativePath, err)
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && function.Name.Name == name {
			return
		}
	}
	t.Fatalf("trace probe %s:%s is missing", relativePath, name)
}
