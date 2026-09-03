package backupsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/backup"
	"bria/internal/backupflow"
	"bria/internal/backupruntime"
	"bria/internal/backupsource"
	"bria/internal/computer"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/settings"
)

func TestCurrentStateExportsSanitizedSemanticSnapshotNotRawStores(t *testing.T) {
	ctx := context.Background()
	barrier := &readBarrier{}
	catalog, _ := computer.NewCatalog()
	if err := catalog.Upsert(computer.Record{ID: "mac", Name: "Mac", Fingerprint: "public-fingerprint", Status: computer.StatusOnline, ProtocolVersion: 1, Capabilities: []computer.Capability{{Provider: domain.ProviderCodex, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	session, err := domain.NewStartingSession("session-1", "intent-1", "mac", domain.ProviderCodex, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	journal := journalPort{
		inputs:  []messagejournal.Input{{MessageID: "pending", SessionID: "session-1", Sequence: 1, Payload: []byte("A"), Phase: messagejournal.InputPending}, {MessageID: "completed", SessionID: "session-1", Sequence: 2, Payload: []byte("B"), Phase: messagejournal.InputCompleted}},
		outputs: []messagejournal.Output{{OperationID: "unknown", SessionID: "session-1", Sequence: 3, Kind: "final", Payload: []byte("C"), Phase: messagejournal.OutputUnknown}, {OperationID: "confirmed", SessionID: "session-1", Sequence: 4, Kind: "final", Payload: []byte("D"), Phase: messagejournal.OutputConfirmed, Receipt: "telegram-secret-receipt"}},
	}
	source, err := backupsource.New(backupsource.Options{
		Barrier: barrier, Settings: settings.NewMemoryStore(), Computers: catalog,
		Sessions: sessionPort{sessions: []domain.Session{session}}, Journal: journal,
		Histories: map[domain.Provider]backupsource.HistorySource{domain.ProviderCodex: history("full codex text"), domain.ProviderClaude: history("unused")},
		Harness:   safeTree{"rules/rules.md": "rules", "settings/settings.json": "{}", "checks/check.go": "package checks", "state/state.json": "{}"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	root := t.TempDir()
	runner, err := backupruntime.NewRunner(backupruntime.RunOptions{WorkDirectory: filepath.Join(root, "work"), LatestPath: filepath.Join(root, "latest"), ComputerID: "mac", State: source, Limits: backupruntime.Limits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20}})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if barrier.begin != 1 || barrier.release != 1 {
		t.Fatalf("barrier = begin %d release %d", barrier.begin, barrier.release)
	}
	restored := filepath.Join(root, "restored")
	if _, err := backup.RestoreCandidate(filepath.Join(root, "latest"), restored); err != nil {
		t.Fatalf("RestoreCandidate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(restored, "messages", "undelivered.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("pending")) || !bytes.Contains(data, []byte("unknown")) || bytes.Contains(data, []byte("completed")) || bytes.Contains(data, []byte("confirmed")) || bytes.Contains(data, []byte("telegram-secret-receipt")) {
		t.Fatalf("undelivered export is not filtered: %s", data)
	}
	if !bytes.Contains(data, []byte(`"source_sequence":1`)) || !bytes.Contains(data, []byte(`"source_sequence":3`)) || bytes.Contains(data, []byte(`"sequence":`)) {
		t.Fatalf("undelivered export does not declare safe remap ordering: %s", data)
	}
	historyBytes, err := os.ReadFile(filepath.Join(restored, "history", "text", "codex", "session-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(historyBytes) != "full codex text" {
		t.Fatalf("history = %q", historyBytes)
	}
	settingsBytes, err := os.ReadFile(filepath.Join(restored, "settings", "bria.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settingsDoc map[string]any
	if err := json.Unmarshal(settingsBytes, &settingsDoc); err != nil || settingsDoc["settings"] == nil {
		t.Fatalf("settings document = %s, %v", settingsBytes, err)
	}
	archive, err := os.ReadFile(filepath.Join(root, "latest"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"telegram-secret-receipt", "BEGIN PRIVATE KEY", "raw journal"} {
		if strings.Contains(string(archive), forbidden) {
			t.Fatalf("backup contains forbidden %q", forbidden)
		}
	}
}

func TestCurrentStateRejectsDuplicateSessionsBeforeExport(t *testing.T) {
	session, err := domain.NewStartingSession("session-1", "intent-1", "mac", domain.ProviderCodex, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	options := validSourceOptions(t, []domain.Session{session, session}, journalPort{})
	source, err := backupsource.New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if transaction, err := source.BeginSnapshot(context.Background()); !errors.Is(err, backupsource.ErrInvalidSource) {
		if transaction != nil {
			_ = transaction.Close()
		}
		t.Fatalf("BeginSnapshot error = %v, want ErrInvalidSource", err)
	}
}

func TestCurrentStateRejectsJournalRecordsFromAnotherSession(t *testing.T) {
	session, err := domain.NewStartingSession("session-1", "intent-1", "mac", domain.ProviderCodex, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	options := validSourceOptions(t, []domain.Session{session}, journalPort{inputs: []messagejournal.Input{{
		MessageID: "message-1", SessionID: "session-other", Sequence: 1, Phase: messagejournal.InputPending,
	}}})
	source, err := backupsource.New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if transaction, err := source.BeginSnapshot(context.Background()); !errors.Is(err, backupsource.ErrInvalidSource) {
		if transaction != nil {
			_ = transaction.Close()
		}
		t.Fatalf("BeginSnapshot error = %v, want ErrInvalidSource", err)
	}
}

func TestCurrentStateBlocksBackupUntilAcceptedInputIsReconciled(t *testing.T) {
	session, err := domain.NewStartingSession("session-1", "intent-1", "mac", domain.ProviderCodex, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	options := validSourceOptions(t, []domain.Session{session}, journalPort{inputs: []messagejournal.Input{{
		MessageID: "accepted", SessionID: "session-1", Sequence: 1, Payload: []byte("exact turn"), Phase: messagejournal.InputAccepted,
	}}})
	barrier := options.Barrier.(*readBarrier)
	source, err := backupsource.New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if transaction, err := source.BeginSnapshot(context.Background()); !errors.Is(err, backupsource.ErrAcceptedInputNeedsReconciliation) {
		if transaction != nil {
			_ = transaction.Close()
		}
		t.Fatalf("BeginSnapshot error = %v, want ErrAcceptedInputNeedsReconciliation", err)
	}
	if barrier.begin != 1 || barrier.release != 1 {
		t.Fatalf("barrier = begin %d release %d", barrier.begin, barrier.release)
	}
}

func TestCurrentStateReturnsSnapshotAndBarrierReleaseFailuresTogether(t *testing.T) {
	readFailure := errors.New("settings unavailable")
	releaseFailure := errors.New("barrier release failed")
	options := validSourceOptions(t, nil, journalPort{})
	options.Barrier = releaseErrorBarrier{err: releaseFailure}
	options.Settings = settingsErrorSource{err: readFailure}
	source, err := backupsource.New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = source.BeginSnapshot(context.Background())
	if !errors.Is(err, readFailure) || !errors.Is(err, releaseFailure) {
		t.Fatalf("BeginSnapshot error = %v, want both read and release failures", err)
	}
}

func TestCurrentStateFailsClosedWhenProviderHistoryContainsSecret(t *testing.T) {
	session, err := domain.NewStartingSession("session-1", "intent-1", "mac", domain.ProviderCodex, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	options := validSourceOptions(t, []domain.Session{session}, journalPort{})
	options.Histories[domain.ProviderCodex] = history(`{"api_key":"must-never-enter-backup"}`)
	source, err := backupsource.New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	root := t.TempDir()
	runner, err := backupruntime.NewRunner(backupruntime.RunOptions{
		WorkDirectory: filepath.Join(root, "work"), LatestPath: filepath.Join(root, "latest"),
		ComputerID: "mac", State: source,
		Limits: backupruntime.Limits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); !errors.Is(err, backupflow.ErrForbiddenContent) {
		t.Fatalf("RunOnce error = %v, want ErrForbiddenContent", err)
	}
	if _, err := os.Stat(filepath.Join(root, "latest")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("latest exists after rejected secret: %v", err)
	}
}

func validSourceOptions(t *testing.T, sessions []domain.Session, journal journalPort) backupsource.Options {
	t.Helper()
	catalog, err := computer.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Upsert(computer.Record{ID: "mac", Name: "Mac", Fingerprint: "public-fingerprint", Status: computer.StatusOnline, ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	return backupsource.Options{
		Barrier: &readBarrier{}, Settings: settings.NewMemoryStore(), Computers: catalog,
		Sessions: sessionPort{sessions: sessions}, Journal: journal,
		Histories: map[domain.Provider]backupsource.HistorySource{domain.ProviderCodex: history("codex"), domain.ProviderClaude: history("claude")},
		Harness:   safeTree{"rules/rules.md": "rules", "settings/settings.json": "{}", "checks/check.go": "package checks", "state/state.json": "{}"},
	}
}

type readBarrier struct{ begin, release int }

func (b *readBarrier) BeginRead(context.Context) (func() error, error) {
	b.begin++
	return func() error { b.release++; return nil }, nil
}

type releaseErrorBarrier struct{ err error }

func (barrier releaseErrorBarrier) BeginRead(context.Context) (func() error, error) {
	return func() error { return barrier.err }, nil
}

type settingsErrorSource struct{ err error }

func (source settingsErrorSource) Current(context.Context) (settings.Snapshot, error) {
	return settings.Snapshot{}, source.err
}

type sessionPort struct{ sessions []domain.Session }

func (s sessionPort) List(context.Context) ([]domain.Session, error) {
	return append([]domain.Session(nil), s.sessions...), nil
}

type journalPort struct {
	inputs  []messagejournal.Input
	outputs []messagejournal.Output
}

func (j journalPort) Inputs(context.Context, string) ([]messagejournal.Input, error) {
	return append([]messagejournal.Input(nil), j.inputs...), nil
}
func (j journalPort) Outputs(context.Context, string) ([]messagejournal.Output, error) {
	return append([]messagejournal.Output(nil), j.outputs...), nil
}

type history string

func (h history) OpenHistory(context.Context, domain.SessionSnapshot) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(h))), nil
}

type safeTree map[string]string

func (tree safeTree) Export(_ context.Context, sink backupruntime.TreeSink) error {
	for name, value := range tree {
		if err := sink.WriteFile(name, strings.NewReader(value)); err != nil {
			return err
		}
	}
	return nil
}
