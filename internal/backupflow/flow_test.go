package backupflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bria/internal/backup"
	"bria/internal/backupflow"
)

func TestCanonicalPlanContainsOnlyTheSixRequiredStateClasses(t *testing.T) {
	layout := backupflow.CanonicalSnapshotLayout()
	got, err := backupflow.CanonicalPlan("macbook", layout)
	if err != nil {
		t.Fatalf("CanonicalPlan: %v", err)
	}
	want := backup.Plan{ComputerID: "macbook", Includes: []backup.Include{
		{Path: "computers/catalog.json", Class: backup.ClassComputers},
		{Path: "harness/checks", Class: backup.ClassHarness},
		{Path: "harness/rules", Class: backup.ClassHarness},
		{Path: "harness/settings", Class: backup.ClassHarness},
		{Path: "harness/state", Class: backup.ClassHarness},
		{Path: "history/text", Class: backup.ClassHistory},
		{Path: "messages/undelivered.json", Class: backup.ClassOutbox},
		{Path: "sessions/state.json", Class: backup.ClassSessions},
		{Path: "settings/bria.json", Class: backup.ClassSettings},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %#v, want %#v", got, want)
	}
}

func TestProductionLayoutUsesCurrentStateSidecarsAndExplicitSemanticExports(t *testing.T) {
	root := t.TempDir()
	sources := backupflow.ProductionSources{
		StatePath:                     filepath.Join(root, "state", "sessions.json"),
		SessionSidecarPaths:           []string{filepath.Join(root, "state", "sessions.json.telegram-reply-routes.json")},
		ComputerCatalogExportPath:     filepath.Join(root, "exports", "computers.json"),
		UndeliveredMessagesExportPath: filepath.Join(root, "exports", "undelivered.json"),
		TextHistoryExportPath:         filepath.Join(root, "exports", "text-history"),
		HarnessRulesPath:              filepath.Join(root, "harness", "rules"),
		HarnessSettingsPath:           filepath.Join(root, "harness", "settings"),
		HarnessChecksPath:             filepath.Join(root, "harness", "checks"),
		HarnessStatePath:              filepath.Join(root, "harness", "state"),
	}
	for _, file := range []string{
		sources.StatePath, sources.StatePath + ".settings.json", sources.SessionSidecarPaths[0],
		sources.ComputerCatalogExportPath, sources.UndeliveredMessagesExportPath,
	} {
		mustWrite(t, file, "{}")
	}
	for _, directory := range []string{
		sources.TextHistoryExportPath, sources.HarnessRulesPath, sources.HarnessSettingsPath,
		sources.HarnessChecksPath, sources.HarnessStatePath,
	} {
		mustWrite(t, filepath.Join(directory, "value.txt"), "safe")
	}

	sourceRoot, layout, err := backupflow.ProductionLayout(sources)
	if err != nil {
		t.Fatalf("ProductionLayout: %v", err)
	}
	if sourceRoot != root {
		t.Fatalf("source root = %q, want %q", sourceRoot, root)
	}
	if layout.Settings != "state/sessions.json.settings.json" || layout.Sessions != "state/sessions.json" ||
		!reflect.DeepEqual(layout.SessionSidecars, []string{"state/sessions.json.telegram-reply-routes.json"}) ||
		layout.UndeliveredMessages != "exports/undelivered.json" {
		t.Fatalf("production layout = %#v", layout)
	}
	plan, err := backupflow.CanonicalPlan("macbook", layout)
	if err != nil {
		t.Fatalf("CanonicalPlan: %v", err)
	}
	if len(plan.Includes) != 10 {
		t.Fatalf("production includes = %d, want 10: %#v", len(plan.Includes), plan.Includes)
	}
	service := backupflow.Service{
		SourceRoot: sourceRoot, LatestPath: filepath.Join(t.TempDir(), "latest.bria-backup"),
		RestoreCandidateDir: filepath.Join(t.TempDir(), "restore"), ComputerID: "macbook", Layout: layout,
	}
	created, err := service.CreateLatest()
	if err != nil {
		t.Fatalf("CreateLatest(production layout): %v", err)
	}
	if len(created.Manifest.Files) != 10 {
		t.Fatalf("production backup files = %d, want 10", len(created.Manifest.Files))
	}

	if err := os.Remove(sources.UndeliveredMessagesExportPath); err != nil {
		t.Fatalf("remove export: %v", err)
	}
	if _, _, err := backupflow.ProductionLayout(sources); !errors.Is(err, backupflow.ErrInvalidLayout) {
		t.Fatalf("missing semantic export error = %v, want ErrInvalidLayout", err)
	}
}

func TestCreateLatestRejectsSecretContentInInnocentlyNamedAllowedFiles(t *testing.T) {
	secretDocuments := map[string]string{
		"telegram bot token": strings.Repeat("ordinary text ", 6000) + "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi_12345",
		"private key":        "-----BEGIN PRIVATE KEY-----\nnot-for-backup\n-----END PRIVATE KEY-----",
		"authorization":      "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.sensitive.signature",
		"secret JSON field":  `{"ordinary":"ok","api_key":"sentinel-secret-value"}`,
		"secret assignment":  "CLAUDE_AUTH_TOKEN=sentinel-secret-value",
		"short secret field": `{"password":"x"}`,
		"binary content":     "text\x01binary",
	}
	for name, document := range secretDocuments {
		t.Run(name, func(t *testing.T) {
			service := populatedService(t)
			mustWrite(t, filepath.Join(service.SourceRoot, "harness", "state", "innocent.txt"), document)
			if _, err := service.CreateLatest(); !errors.Is(err, backupflow.ErrForbiddenContent) {
				t.Fatalf("CreateLatest error = %v, want ErrForbiddenContent", err)
			}
			if _, err := os.Stat(service.LatestPath); !os.IsNotExist(err) {
				t.Fatalf("secret-bearing backup was promoted: %v", err)
			}
		})
	}
}

func TestCreateLatestKeepsSafeSecretReferencesAndStreamingUTF8(t *testing.T) {
	service := populatedService(t)
	safe := strings.Repeat("x", (64<<10)-1) + "Я" + ` {"telegram_token":{"env_var":"BRIA_TELEGRAM_TOKEN"},"callback_key":{"secret_file":"/run/secrets/callback"}}`
	mustWrite(t, filepath.Join(service.SourceRoot, "harness", "state", "references.txt"), safe)
	if _, err := service.CreateLatest(); err != nil {
		t.Fatalf("safe references rejected: %v", err)
	}
}

func TestSecretContentCannotReplaceTheLastVerifiedCopy(t *testing.T) {
	service := populatedService(t)
	if _, err := service.CreateLatest(); err != nil {
		t.Fatalf("CreateLatest(good): %v", err)
	}
	before, err := os.ReadFile(service.LatestPath)
	if err != nil {
		t.Fatalf("read good latest: %v", err)
	}
	mustWrite(t, filepath.Join(service.SourceRoot, "harness", "state", "innocent.txt"), `{"api_key":"must-never-enter-backup"}`)
	if _, err := service.CreateLatest(); !errors.Is(err, backupflow.ErrForbiddenContent) {
		t.Fatalf("CreateLatest(secret) error = %v, want ErrForbiddenContent", err)
	}
	after, err := os.ReadFile(service.LatestPath)
	if err != nil {
		t.Fatalf("read preserved latest: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("secret-bearing candidate changed the last verified copy")
	}
}

func TestCreateLatestRejectsEveryForbiddenDataClass(t *testing.T) {
	forbidden := []string{
		"secrets", "credentials", "auth", "tokens", "keys", "logs", "media",
		"photos", "files", "archives", "projects", "binaries", "parakeet", "cache",
		"telegram-token.json", "codex-auth.json", "computer-keys.json",
	}
	for _, name := range forbidden {
		t.Run(name, func(t *testing.T) {
			service := populatedService(t)
			mustWrite(t, filepath.Join(service.SourceRoot, "harness", "state", name, "payload"), "forbidden")
			if _, err := service.CreateLatest(); !errors.Is(err, backupflow.ErrForbiddenPath) {
				t.Fatalf("CreateLatest error = %v, want ErrForbiddenPath", err)
			}
		})
	}
}

func TestCanonicalPlanRejectsAForbiddenOrOverlappingLayout(t *testing.T) {
	forbidden := backupflow.CanonicalSnapshotLayout()
	forbidden.HarnessState = "harness/secrets"
	if _, err := backupflow.CanonicalPlan("macbook", forbidden); !errors.Is(err, backupflow.ErrForbiddenPath) {
		t.Fatalf("forbidden layout error = %v, want ErrForbiddenPath", err)
	}

	overlapping := backupflow.CanonicalSnapshotLayout()
	overlapping.HarnessRules = "harness"
	if _, err := backupflow.CanonicalPlan("macbook", overlapping); !errors.Is(err, backupflow.ErrInvalidLayout) {
		t.Fatalf("overlapping layout error = %v, want ErrInvalidLayout", err)
	}
}

func TestCreateLatestReplacesOnlyTheVerifiedLatestCopy(t *testing.T) {
	service := populatedService(t)
	first, err := service.CreateLatest()
	if err != nil {
		t.Fatalf("first CreateLatest: %v", err)
	}
	if _, err := backup.Validate(first.Path); err != nil {
		t.Fatalf("validate first latest: %v", err)
	}
	mustWrite(t, filepath.Join(service.SourceRoot, "settings", "bria.json"), "settings-v2")
	second, err := service.CreateLatest()
	if err != nil {
		t.Fatalf("second CreateLatest: %v", err)
	}
	if second.Path != service.LatestPath || second.Fingerprint == first.Fingerprint {
		t.Fatalf("second copy = %#v, first fingerprint %q", second, first.Fingerprint)
	}
	if _, err := os.Stat(service.LatestPath + ".candidate"); !os.IsNotExist(err) {
		t.Fatalf("transient candidate remains: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(service.LatestPath))
	if err != nil {
		t.Fatalf("read backup directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(service.LatestPath) {
		t.Fatalf("managed backup directory = %#v, want only latest", entries)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err := backup.RestoreCandidate(service.LatestPath, restored); err != nil {
		t.Fatalf("restore latest: %v", err)
	}
	assertContent(t, filepath.Join(restored, "settings", "bria.json"), "settings-v2")
}

func TestBackupAndRestoreWorkingPathsCannotContaminateEligibleState(t *testing.T) {
	service := populatedService(t)
	service.LatestPath = filepath.Join(service.SourceRoot, "history", "text", "latest.bria-backup")
	if _, err := service.CreateLatest(); !errors.Is(err, backupflow.ErrInvalidLayout) {
		t.Fatalf("latest inside eligible state error = %v, want ErrInvalidLayout", err)
	}

	service = populatedService(t)
	if _, err := service.CreateLatest(); err != nil {
		t.Fatalf("CreateLatest: %v", err)
	}
	service.RestoreCandidateDir = filepath.Join(service.SourceRoot, "harness", "state", "restore-candidate")
	if _, err := service.PrepareRestore(); !errors.Is(err, backupflow.ErrInvalidLayout) {
		t.Fatalf("restore inside eligible state error = %v, want ErrInvalidLayout", err)
	}
}

func TestPrepareRestoreRejectsAValidButNonCanonicalBackupBeforeExtraction(t *testing.T) {
	service := populatedService(t)
	foreignCandidate := service.LatestPath + ".foreign"
	if _, err := backup.BuildCandidate(service.SourceRoot, foreignCandidate, backup.Plan{
		ComputerID: service.ComputerID,
		Includes:   []backup.Include{{Path: "settings/bria.json", Class: backup.ClassSettings}},
	}); err != nil {
		t.Fatalf("build foreign backup: %v", err)
	}
	if _, err := backup.PromoteCandidate(foreignCandidate, service.LatestPath); err != nil {
		t.Fatalf("promote foreign backup: %v", err)
	}
	if _, err := service.PrepareRestore(); !errors.Is(err, backup.ErrInvalidBackup) {
		t.Fatalf("PrepareRestore error = %v, want ErrInvalidBackup", err)
	}
	if _, err := os.Stat(service.RestoreCandidateDir); !os.IsNotExist(err) {
		t.Fatalf("noncanonical backup was extracted: %v", err)
	}
}

func TestRestoreNeedsExactVerifiedActivationReceipt(t *testing.T) {
	service := populatedService(t)
	if _, err := service.CreateLatest(); err != nil {
		t.Fatalf("CreateLatest: %v", err)
	}
	prepared, err := service.PrepareRestore()
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	activator := &recordingActivator{}
	receipt, err := service.ActivateRestore(context.Background(), prepared, activator)
	if err != nil {
		t.Fatalf("ActivateRestore: %v", err)
	}
	if activator.calls != 1 || receipt.ReceiptID != "activation-1" || receipt.Fingerprint != prepared.Fingerprint {
		t.Fatalf("activation = calls %d receipt %#v", activator.calls, receipt)
	}

	bad := &recordingActivator{mismatch: true}
	if _, err := service.ActivateRestore(context.Background(), prepared, bad); !errors.Is(err, backupflow.ErrInvalidReceipt) {
		t.Fatalf("mismatched receipt error = %v, want ErrInvalidReceipt", err)
	}

	mustWrite(t, filepath.Join(prepared.CandidateDir, "settings", "bria.json"), "tampered")
	notCalled := &recordingActivator{}
	if _, err := service.ActivateRestore(context.Background(), prepared, notCalled); err == nil {
		t.Fatal("tampered restore candidate accepted")
	}
	if notCalled.calls != 0 {
		t.Fatalf("activator called %d times for tampered candidate", notCalled.calls)
	}
}

type recordingActivator struct {
	calls    int
	mismatch bool
}

func (a *recordingActivator) Activate(_ context.Context, request backupflow.ActivationRequest) (backupflow.ActivationReceipt, error) {
	a.calls++
	receipt := backupflow.ActivationReceipt{ReceiptID: "activation-1", CandidateDir: request.CandidateDir, ComputerID: request.ComputerID, Fingerprint: request.Fingerprint}
	if a.mismatch {
		receipt.Fingerprint = "wrong"
	}
	return receipt, nil
}

func populatedService(t *testing.T) backupflow.Service {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	for path, content := range map[string]string{
		"settings/bria.json":           "settings-v1",
		"computers/catalog.json":       "computers",
		"sessions/state.json":          "sessions",
		"messages/undelivered.json":    "outbox",
		"history/text/session.jsonl":   "history",
		"harness/rules/rules.md":       "rules",
		"harness/settings/config.json": "harness settings",
		"harness/checks/check.go":      "checks",
		"harness/state/state.json":     "harness state",
	} {
		mustWrite(t, filepath.Join(source, filepath.FromSlash(path)), content)
	}
	return backupflow.Service{
		SourceRoot: source, LatestPath: filepath.Join(root, "backup", "latest.bria-backup"),
		RestoreCandidateDir: filepath.Join(root, "restore", "candidate"), ComputerID: "macbook",
		Layout: backupflow.CanonicalSnapshotLayout(),
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}
