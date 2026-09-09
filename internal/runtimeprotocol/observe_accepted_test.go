package runtimeprotocol_test

import (
	"encoding/json"
	"testing"

	"bria/internal/runtimeprotocol"
)

func TestObserveAcceptedHasIdentityButCannotCarryInput(t *testing.T) {
	base := map[string]any{"protocol": 1, "type": "observe_accepted", "request_id": "observe-1", "message_id": "telegram-update:42"}
	decode := func(fields map[string]any) error {
		line, _ := json.Marshal(fields)
		message, err := runtimeprotocol.DecodeParentLine(line, runtimeprotocol.Limits{})
		if err == nil {
			_, err = runtimeprotocol.EncodeParentLine(message, runtimeprotocol.Limits{})
		}
		return err
	}
	if err := decode(base); err != nil {
		t.Fatalf("read-only observation envelope rejected: %v", err)
	}
	for _, key := range []string{"text", "command", "key", "model", "effort", "attachments", "interaction_response"} {
		base[key] = ""
		if err := decode(base); err == nil {
			t.Errorf("observation allowed input field %s", key)
		}
		delete(base, key)
	}
	delete(base, "message_id")
	if err := decode(base); err == nil {
		t.Fatal("observation accepted missing message identity")
	}
}

func TestDetachIsDistinctFromClosingTerminal(t *testing.T) {
	line := []byte(`{"protocol":1,"type":"detach"}`)
	message, err := runtimeprotocol.DecodeParentLine(line, runtimeprotocol.Limits{})
	if err != nil || message.Type == runtimeprotocol.TypeClose {
		t.Fatalf("detach is not a distinct valid control: %v", err)
	}
	if _, err := runtimeprotocol.EncodeParentLine(message, runtimeprotocol.Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestObservedEventRetainsStableIdentity(t *testing.T) {
	line := []byte(`{"protocol":1,"type":"event","request_id":"r-1","kind":"commentary","text":"progress","event_id":"8123:0"}`)
	message, err := runtimeprotocol.DecodeAdapterLine(line, runtimeprotocol.Limits{})
	if err != nil {
		t.Fatalf("stable observed event rejected: %v", err)
	}
	encoded, err := runtimeprotocol.EncodeAdapterLine(message, runtimeprotocol.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil || wire["event_id"] != "8123:0" {
		t.Fatal("observation lost durable event identity")
	}
}
