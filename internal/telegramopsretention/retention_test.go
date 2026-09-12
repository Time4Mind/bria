package telegramopsretention_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"bria/internal/telegramopsretention"
)

func TestCompactPreservesEveryNonFinalizedRecordBeyondHistoryLimit(t *testing.T) {
	const limit = 64
	snapshot := telegramopsretention.Snapshot{
		Operations:       make(map[string]json.RawMessage),
		Statuses:         make(map[string]json.RawMessage),
		Acknowledgements: make(map[string]json.RawMessage),
	}
	for sequence := 1; sequence <= limit+20; sequence++ {
		id := fmt.Sprintf("status:%d", sequence)
		snapshot.Operations[id] = json.RawMessage(fmt.Sprintf(`{"id":%q,"update_id":%d,"phase":"effect_unknown"}`, id, sequence))
		snapshot.Statuses[id] = json.RawMessage(fmt.Sprintf(`{"id":%q,"sequence":%d,"phase":"send_unknown"}`, id, sequence))
		snapshot.Acknowledgements[id] = json.RawMessage(fmt.Sprintf(`{"operation_id":%q,"callback_query_id":"query","phase":"pending"}`, id))
	}

	telegramopsretention.Compact(snapshot, limit)

	if len(snapshot.Operations) != limit+20 || len(snapshot.Statuses) != limit+20 || len(snapshot.Acknowledgements) != limit+20 {
		t.Fatalf("active records were pruned: callbacks=%d statuses=%d acknowledgements=%d",
			len(snapshot.Operations), len(snapshot.Statuses), len(snapshot.Acknowledgements))
	}
}

func TestCompactPinsCommittedCounterpartOfNonFinalizedOperation(t *testing.T) {
	const limit = 2
	snapshot := telegramopsretention.Snapshot{
		Operations: map[string]json.RawMessage{
			"status:1": json.RawMessage(`{"id":"status:1","update_id":1,"phase":"committed"}`),
			"status:2": json.RawMessage(`{"id":"status:2","update_id":2,"phase":"committed"}`),
			"status:3": json.RawMessage(`{"id":"status:3","update_id":3,"phase":"committed"}`),
		},
		Statuses: map[string]json.RawMessage{
			"status:1": json.RawMessage(`{"id":"status:1","sequence":1,"phase":"send_unknown"}`),
		},
		Acknowledgements: make(map[string]json.RawMessage),
	}

	telegramopsretention.Compact(snapshot, limit)

	if _, pinned := snapshot.Operations["status:1"]; !pinned {
		t.Fatal("committed callback receipt needed by non-finalized status was pruned")
	}
	if len(snapshot.Operations) != limit {
		t.Fatalf("callback history=%d want bounded %d including pinned counterpart", len(snapshot.Operations), limit)
	}
}
