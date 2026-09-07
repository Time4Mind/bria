package telegramcontroller

import (
	"context"
	"reflect"
	"testing"

	"bria/internal/domain"
)

func TestArchiveInMemoryUsesPromptIndexesNotAssistantEmoji(t *testing.T) {
	id := domain.SessionID("session")
	c := &Controller{
		history:       map[domain.SessionID][]string{id: {"👨‍💻 assistant quoting prompt", "🙋‍♂ first caption", "tool output", "👨‍💻 processed second", "🙅‍♂ third", "👨‍💻 fourth"}},
		promptIndexes: map[domain.SessionID]map[string]int{id: {"m1": 1, "m2": 3, "m3": 4, "m4": 5}},
	}
	if got := c.archivePromptDescription(context.Background(), id); !reflect.DeepEqual(got, []string{"first caption", "processed second"}) {
		t.Fatalf("keyed in-memory prompts: %#v", got)
	}
	c.promptIndexes = nil
	if got := c.archivePromptDescription(context.Background(), id); len(got) != 0 {
		t.Fatalf("unknown history guessed user role from emoji: %#v", got)
	}
}
