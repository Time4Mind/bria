package nativeadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
)

func TestObserveAcceptedReadsExactTurnWithoutTerminalInput(t *testing.T) {
	a, output := correlationAdapter(t)
	a.active, a.steers = nil, nil
	a.config.Provider, a.config.Workdir = domain.ProviderCodex, t.TempDir()
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	if err := os.Mkdir(filepath.Join(root, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]string{"id": a.id, "cwd": a.config.Workdir}})
	body := string(meta) + "\n" +
		`{"type":"turn_context","payload":{"turn_id":"foreign"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","message":"foreign","phase":"final_answer"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"foreign"}}` + "\n" +
		`{"type":"turn_context","payload":{"turn_id":"accepted-turn"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","message":"progress","phase":"commentary"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","message":"finished","phase":"final_answer"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"accepted-turn"}}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "sessions", "rollout-"+a.id+".jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.acceptReceipt("accepted-42", "accepted-turn"); err != nil {
		t.Fatal(err)
	}
	r := runtimeprotocol.ParentMessage{Protocol: 1, Type: runtimeprotocol.TypeObserveAccepted, RequestID: "observe-1", MessageID: "accepted-42"}
	if err := a.handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	defer a.reader.Close()
	if err := a.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	scan := json.NewDecoder(output)
	var messages []runtimeprotocol.AdapterMessage
	for scan.More() {
		var m runtimeprotocol.AdapterMessage
		if err := scan.Decode(&m); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	if len(messages) != 4 || messages[0].Type != runtimeprotocol.TypeAccepted || messages[1].EventID == "" || messages[1].Text != "progress" || messages[2].Text != "finished" || messages[3].Type != runtimeprotocol.TypeCompleted {
		t.Fatalf("%+v", messages)
	}
	if a.receipts["accepted-42"] != "completed" {
		t.Fatal("missing durable terminal receipt")
	}
}

func TestObserveAcceptedRejectsMissingCorrelationBeforeOpeningReader(t *testing.T) {
	a, output := correlationAdapter(t)
	a.active, a.steers = nil, nil
	if err := a.handle(context.Background(), runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeObserveAccepted, RequestID: "o", MessageID: "unknown"}); err == nil {
		t.Fatal("unknown receipt attached")
	}
	if output.Len() != 0 || a.reader != nil || a.active != nil {
		t.Fatal("unknown correlation changed observer")
	}
}
