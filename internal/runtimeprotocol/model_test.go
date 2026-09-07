package runtimeprotocol_test

import (
	"bria/internal/runtimeprotocol"
	"strings"
	"testing"
)

func TestSubmitAcceptsBoundedModelAndEffortOnlyForNewTurn(t *testing.T) {
	line := []byte(`{"protocol":1,"type":"submit","request_id":"r1","text":"hello","model":"available-model","effort":"high"}`)
	message, err := runtimeprotocol.DecodeParentLine(line, runtimeprotocol.Limits{})
	if err != nil {
		t.Fatalf("new-turn model selection rejected: %v", err)
	}
	encoded, err := runtimeprotocol.EncodeParentLine(message, runtimeprotocol.Limits{})
	if err != nil || !strings.Contains(string(encoded), `"model":"available-model"`) || !strings.Contains(string(encoded), `"effort":"high"`) {
		t.Fatalf("selection lost: %s, %v", encoded, err)
	}
	for _, invalid := range []string{strings.Replace(string(line), `"submit"`, `"steer"`, 1), strings.Replace(string(line), "available-model", "bad model", 1), strings.Replace(string(line), "available-model", strings.Repeat("x", 257), 1)} {
		if _, err := runtimeprotocol.DecodeParentLine([]byte(invalid), runtimeprotocol.Limits{}); err == nil {
			t.Fatal("invalid model selection accepted")
		}
	}
}
