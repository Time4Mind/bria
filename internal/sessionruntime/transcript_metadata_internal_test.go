package sessionruntime

import "testing"

func TestDecodeWirePreservesStructuredTranscriptMetadata(t *testing.T) {
	message, err := decodeWire([]byte(`{"protocol":1,"type":"event","request_id":"turn-1","kind":"tool","text":"Read","event_metadata":{"item_id":"tool-1","name":"Read","arguments":"README.md","result":"ok","status":"completed"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if message.EventMetadata == nil || message.EventMetadata.ItemID != "tool-1" || message.EventMetadata.Arguments != "README.md" ||
		message.EventMetadata.Result != "ok" || message.EventMetadata.Status != "completed" {
		t.Fatalf("metadata = %#v", message.EventMetadata)
	}
}
