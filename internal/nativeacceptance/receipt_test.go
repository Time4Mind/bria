package nativeacceptance_test

import (
	"reflect"
	"strings"
	"testing"

	"bria/internal/nativeacceptance"
)

func TestAcceptanceDocumentPreservesLegacyAndAtomicCorrelation(t *testing.T) {
	old, err := nativeacceptance.Decode([]byte(`{"session_id":"s","receipts":{"old":"unknown","done":"completed","bad":"failed"}}`), "s")
	if err != nil {
		t.Fatal(err)
	}
	next, err := nativeacceptance.Accept(old, "new", "native-turn")
	if err != nil {
		t.Fatal(err)
	}
	if old.Receipts["new"] != "" || old.TurnIDs["new"] != "" {
		t.Fatal("unpersisted acceptance changed previous snapshot")
	}
	data, err := nativeacceptance.Encode(next)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := nativeacceptance.Decode(data, "s")
	if err != nil || !reflect.DeepEqual(next, reopened) || reopened.Receipts["new"] != "unknown" || reopened.TurnIDs["new"] != "native-turn" || reopened.TurnIDs["old"] != "" {
		t.Fatalf("correlation lost or invented: %#v %v", reopened, err)
	}
	for _, key := range []string{"old", "done", "bad", "new"} {
		if _, err := nativeacceptance.Accept(reopened, key, "different-turn"); err == nil {
			t.Fatal("reused durable message identity was remapped")
		}
	}
	next.Receipts["new"] = "completed"
	same, err := nativeacceptance.Accept(next, "new", "native-turn")
	if err != nil || same.Receipts["new"] != "completed" {
		t.Fatal("repeated acceptance downgraded terminal outcome")
	}
}

func TestAcceptanceDocumentRejectsAmbiguousOrUnboundedInput(t *testing.T) {
	for _, data := range []string{
		`{"session_id":"s","session_id":"s","receipts":{}}`,
		`{"session_id":"s","receipts":{"m":"unknown","m":"completed"}}`,
		`{"session_id":"s","receipts":{"m":"unknown"},"turn_ids":{"m":"one","m":"two"}}`,
		`{"session_id":"s","receipts":{"m":"unknown"},"turn_ids":{"absent":"turn"}}`,
		`{"session_id":"s","receipts":{"m":"unknown"},"turn_ids":{"m":""}}`,
		`{"session_id":"s","receipts":{"m":"unknown"},"turn_ids":{"m":"\n"}}`,
		`{"session_id":"s","receipts":{}} {}`,
		`{"session_id":"s","receipts":null}`,
		`{"session_id":"other","receipts":{}}`,
		`{"session_id":"s","receipts":{},"extra":{}}`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		if _, err := nativeacceptance.Decode([]byte(data), "s"); err == nil {
			t.Fatal("ambiguous acceptance accepted")
		}
	}
	doc := nativeacceptance.Document{SessionID: "s", Receipts: map[string]string{"m": "unknown"}, TurnIDs: map[string]string{"m": strings.Repeat("x", 1025)}}
	if _, err := nativeacceptance.Encode(doc); err == nil {
		t.Fatal("unbounded correlation persisted")
	}
}
