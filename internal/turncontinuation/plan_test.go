package turncontinuation_test

import (
	"bria/internal/domain"
	"bria/internal/turncontinuation"
	"bria/internal/turnprocessing"
	"context"
	"testing"
)

func member(message, turn string, sequence uint64) turncontinuation.Member {
	return turncontinuation.Member{Input: turnprocessing.DurableLeasedInput{SessionID: "s", MessageID: message, Sequence: sequence}, TurnID: turn, Accepted: true, Callbacks: turnprocessing.DurableInputCallbacks{OnCompleted: func(context.Context, turnprocessing.DurableInputProcessReceipt) error { return nil }}}
}

func TestContinuationClaimCannotAliasDifferentOpaqueIdentities(t *testing.T) {
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "native", Generation: 2}
	first, err := turncontinuation.Plan([]turncontinuation.Member{member("c", "a\x00b", 1)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := turncontinuation.Plan([]turncontinuation.Member{member("b\x00c", "a", 1)})
	if err != nil {
		t.Fatal(err)
	}
	prior, start, err := turncontinuation.Claim(nil, binding, first, false)
	if err != nil || !start {
		t.Fatal("first claim rejected")
	}
	if _, _, err := turncontinuation.Claim(prior, binding, second, false); err == nil {
		t.Fatal("different exact tuple accepted as idempotent duplicate")
	}
}
func TestPlanGroupsExactNativeTurnInJournalOrder(t *testing.T) {
	input := []turncontinuation.Member{member("next", "b", 9), member("steer", "a", 8), member("root", "a", 3)}
	groups, err := turncontinuation.Plan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || len(groups[0].Members) != 2 || groups[0].Members[0].Input.MessageID != "root" || groups[0].Members[1].Input.MessageID != "steer" || groups[1].Members[0].Input.MessageID != "next" {
		t.Fatal("lost exact grouping or oldest root")
	}
	groups[0].Members[0].Input.MessageID = "changed"
	if input[2].Input.MessageID != "root" {
		t.Fatal("plan aliases caller")
	}
}
func TestPlanRejectsAmbiguousBatchBeforeExecution(t *testing.T) {
	for _, inputs := range [][]turncontinuation.Member{
		{member("a", "", 1), member("b", "", 2)},
		{member("a", "native", 1), member("a", "native", 2)},
		{member("a", "native", 1), member("b", "other", 1)},
	} {
		if _, err := turncontinuation.Plan(inputs); err == nil {
			t.Fatal("ambiguous batch accepted")
		}
	}
	legacy := member("a", "", 1)
	if _, err := turncontinuation.Plan([]turncontinuation.Member{legacy}); err != nil {
		t.Fatal(err)
	}
	legacy.Accepted = false
	if _, err := turncontinuation.Plan([]turncontinuation.Member{legacy}); err == nil {
		t.Fatal("unknown without native proof accepted")
	}
}
