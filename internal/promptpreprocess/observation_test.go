package promptpreprocess

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSuccessObservationCarriesReceiptButNeverPrompt(t *testing.T) {
	request := Request{ComputerID: "local", SessionID: "session", MessageID: "message", Instruction: "private-instruction", Text: "private-text"}
	result := Result{Text: "private-result", Provider: "codex", Model: "gpt-5.6-luna", ModelEvidence: "codex_cli_header"}
	observation := SuccessObservation(request, result)
	if observation.Stage != StageComplete || observation.Category != CategorySuccess || observation.Attempts != 1 || observation.ModelEvidence != result.ModelEvidence || observation.Model != result.Model || observation.Error != "" {
		t.Fatal("success observation semantics mismatch")
	}
	encoded, err := json.Marshal(observation)
	if err != nil || strings.Contains(string(encoded), "private-") {
		t.Fatal("observation included prompt/result content")
	}
	result.ModelEvidence = ""
	if SuccessObservation(request, result).ModelEvidence != "" {
		t.Fatal("requested model was promoted to confirmed evidence")
	}
}

func TestCachedObservationNeverClaimsFreshInvocation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		observation := CachedObservation(Request{ComputerID: "local", SessionID: "session", MessageID: "message"}, failed)
		if observation.Attempts != 0 || observation.Stage != StageCache || observation.Model != "" || observation.ModelEvidence != "" || observation.Provider != "" {
			t.Fatal("cached result claimed execution evidence")
		}
		if failed && observation.Category != CategoryCachedFallback || !failed && observation.Category != CategoryCached {
			t.Fatal("cached fallback classification lost")
		}
	}
}
