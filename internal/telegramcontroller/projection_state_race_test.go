package telegramcontroller_test

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"bria/internal/domain"
)

func TestProjectionUIStateConcurrentPromptAndHistory(t *testing.T) {
	ctx := context.Background()
	id := domain.SessionID("projection-session")
	state := &projectionUIState{}
	if err := state.SetCardPrompt(ctx, id, "input", "initial prompt"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 100; i++ {
			if err := state.SetCardPrompt(ctx, id, "input", "updated prompt"); err != nil {
				t.Error(err)
			}
			if _, err := state.LoadCardHistory(ctx, id); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 100; i++ {
			if err := state.AppendCardHistory(ctx, id, "event-"+strconv.Itoa(i)); err != nil {
				t.Error(err)
			}
		}
	}()
	close(start)
	workers.Wait()
	history, err := state.LoadCardHistory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 101 || history[0] != "updated prompt" {
		t.Fatalf("prompt replacement or event custody lost: %q", history)
	}
	for i, item := range history[1:] {
		if item != "event-"+strconv.Itoa(i) {
			t.Fatalf("event order lost at %d: %q", i, item)
		}
	}
	history[0] = "mutated snapshot"
	again, err := state.LoadCardHistory(ctx, id)
	if err != nil || strings.Contains(strings.Join(again, "\n"), "mutated snapshot") {
		t.Fatalf("snapshot aliases stored history: %q %v", again, err)
	}
}
