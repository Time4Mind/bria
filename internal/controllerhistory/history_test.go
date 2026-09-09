package controllerhistory_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"bria/internal/controllerhistory"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

func TestAppendPreservesQueuedPromptAndDisplayIsAnIndependentProjection(t *testing.T) {
	values := map[domain.SessionID][]string{"s": {"root prompt", "queued prompt"}}
	prompts := map[domain.SessionID]map[string]int{"s": {"root": 0, "queued": 1}}
	var kinds map[domain.SessionID]map[int]string
	var technical map[domain.SessionID]map[int]bool
	var tails map[domain.SessionID]map[string]int
	history := controllerhistory.History{Mu: &sync.Mutex{}, Values: &values, Prompts: &prompts, Kinds: &kinds, Technical: &technical, Tails: &tails}
	ctx := context.Background()
	for _, text := range []string{"progress one", "progress two"} {
		history.Append(ctx, "s", "root", sessionruntime.TurnEvent{Kind: sessionruntime.EventCommentary, Text: text})
	}
	want := []string{"root prompt", "progress one", "progress two", "queued prompt"}
	if !reflect.DeepEqual(values["s"], want) {
		t.Fatal("runtime history crossed queued prompt")
	}
	blocks, err := history.Display(ctx, "s", values["s"])
	if err != nil || len(blocks) != 4 || blocks[0].Kind != "prompt" || blocks[3].Kind != "prompt" {
		t.Fatal("display lost prompt identity")
	}
	blocks[1].Text = "changed projection"
	if !reflect.DeepEqual(values["s"], want) {
		t.Fatal("display mutated retained history")
	}
}
