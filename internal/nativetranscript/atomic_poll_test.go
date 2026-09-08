package nativetranscript

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Cancellation is injected at a deterministic read checkpoint, after a whole
// event and inside the next record. Assertions use only the Reader API.
type checkpointContext struct {
	context.Context
	remaining int
}

func TestPollFailedBatchRetainsEventsAndFinalDedupState(t *testing.T) {
	final := `{"type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final"}}` + "\n"
	opts, path := fixture(t, "codex", codexMeta()+final+"invalid\n")
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for attempt := 0; attempt < 2; attempt++ {
		if events, err := r.Poll(context.Background()); !errors.Is(err, ErrMalformed) || len(events) != 0 {
			t.Fatalf("failed batch events=%v, err=%v", events, err)
		}
	}
	// Repair the synthetic same-inode file, then retry through the public seam.
	mirror := `{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final","content":"answer"}}` + "\n"
	if err := os.WriteFile(path, []byte(codexMeta()+final+mirror), 0600); err != nil {
		t.Fatal(err)
	}
	events, err := r.Poll(context.Background())
	if err != nil || len(events) != 1 || events[0].Kind != KindFinal || events[0].Text != "answer" {
		t.Fatalf("repaired retry events=%v, err=%v", events, err)
	}
}

func TestPollCancellationWithPendingLargeRecord(t *testing.T) {
	large := `{"type":"event_msg","payload":{"type":"item_completed","item":{"stdout":"` + strings.Repeat("x", 5000) + `"}}}` + "\n"
	final := `{"type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final"}}` + "\n"
	opts, _ := fixture(t, "codex", codexMeta()+large+final)
	opts.MaxLineBytes, opts.MaxPollBytes = 128, 256
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if events, err := r.Poll(context.Background()); err != nil || len(events) != 0 {
		t.Fatalf("initial partial events=%v, err=%v", events, err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		ctx := &checkpointContext{Context: context.Background(), remaining: 100}
		if events, err := r.Poll(ctx); !errors.Is(err, context.Canceled) || len(events) != 0 {
			t.Fatalf("cancelled partial events=%v, err=%v", events, err)
		}
	}
	var got []Event
	for poll := 0; poll < 30; poll++ {
		events, err := r.Poll(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, events...)
	}
	baseline, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer baseline.Close()
	var want []Event
	for poll := 0; poll < 30; poll++ {
		events, err := baseline.Poll(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, events...)
	}
	if len(want) != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("retry=%+v; baseline=%+v", got, want)
	}
}

func (c *checkpointContext) Err() error {
	if c.remaining == 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}

func TestPollCancellationRetriesWholeBatch(t *testing.T) {
	first := `{"type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final"}}` + "\n"
	second := `{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	opts, _ := fixture(t, "codex", codexMeta()+first+second)
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := &checkpointContext{Context: context.Background(), remaining: 1 + len(codexMeta()) + len(first) + 10}
	if events, err := r.Poll(ctx); !errors.Is(err, context.Canceled) || len(events) != 0 {
		t.Fatalf("cancelled poll: %v, %v", events, err)
	}
	got, err := r.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer baseline.Close()
	want, err := baseline.Poll(context.Background())
	if err != nil || len(want) != 2 || !reflect.DeepEqual(got, want) {
		t.Fatalf("retry=%+v; baseline=%+v; err=%v", got, want, err)
	}
	if events, err := r.Poll(context.Background()); err != nil || len(events) != 0 {
		t.Fatalf("duplicate events=%v, err=%v", events, err)
	}
}
