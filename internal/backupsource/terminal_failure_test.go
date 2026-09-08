package backupsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"bria/internal/backupsource"
	"bria/internal/domain"
	"bria/internal/messagejournal"
)

func TestBackupDoesNotRequeueProvenTerminalFailure(t *testing.T) {
	ctx := context.Background()
	session, err := domain.NewStartingSession("session-1", "intent-1", "mac", domain.ProviderCodex, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	inputs := []messagejournal.Input{}
	for i, phase := range []messagejournal.InputPhase{messagejournal.InputTerminalFailed, messagejournal.InputPending, messagejournal.InputFailed, messagejournal.InputUnknown} {
		inputs = append(inputs, messagejournal.Input{SessionID: "session-1", MessageID: string(phase), Sequence: uint64(i + 1), Phase: phase})
	}
	source, err := backupsource.New(validSourceOptions(t, []domain.Session{session}, journalPort{inputs: inputs}))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.BeginSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot rejected a proven terminal failure: %v", err)
	}
	defer snapshot.Close()
	var payload bytes.Buffer
	if err := snapshot.Sources().UndeliveredMessages.Export(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	var restored backupsource.UndeliveredState
	if err := json.Unmarshal(payload.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.Sessions) != 1 || len(restored.Sessions[0].Records) != 3 {
		t.Fatalf("terminal failure requeued or pending records lost: %#v", restored)
	}
	for i, record := range restored.Sessions[0].Records {
		want := inputs[i+1]
		if record.Input == nil || record.Input.MessageID != want.MessageID || record.Input.Phase != want.Phase || record.SourceSequence != want.Sequence {
			t.Fatalf("changed undelivered identity/order: %#v", record)
		}
	}
}
