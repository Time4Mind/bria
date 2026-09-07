package codex

import "testing"

func TestAdapterDecodesNewTurnModelSelection(t *testing.T) {
	request, err := decodeAdapterRequest([]byte(`{"protocol":1,"type":"submit","request_id":"r1","text":"hello","model":"available-model","effort":"high"}`))
	if err != nil || request.Model != "available-model" || request.Effort != "high" {
		t.Fatalf("selection lost at adapter boundary: %#v, %v", request, err)
	}
}
