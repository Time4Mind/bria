package nativetranscript

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestScanVisitsCompletePrefix(t *testing.T) {
	opts, _ := fixture(t, "codex", codexMeta()+`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn1"}}`+"\n")
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var got []Event
	err = r.Scan(context.Background(), func(events []Event) error { got = append(got, events...); return nil })
	if err != nil || len(got) != 1 || got[0].Kind != KindComplete || got[0].TurnID != "turn1" {
		t.Fatalf("scan=%+v, err=%v", got, err)
	}
}

func TestScanRequiresCompletePrefixWithinBudget(t *testing.T) {
	for _, test := range []struct {
		name, body string
		want       error
	}{
		{"partial", codexMeta() + `{"type":"ignored"}`, ErrNotFound},
		{"budget", codexMeta() + strings.Repeat(`{"type":"ignored"}`+"\n", 3000), ErrLimit},
		{"malformed", codexMeta() + `{"type":"event_msg","payload":{"type":"task_complete"}}` + "\nnot json\n", ErrMalformed},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, _ := fixture(t, "codex", test.body)
			opts.MaxLineBytes, opts.MaxPollBytes = 128, 256
			r, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			err = r.Scan(context.Background(), func([]Event) error { return nil })
			if !errors.Is(err, test.want) {
				t.Fatalf("accepted incomplete prefix: %v", err)
			}
			var recordErr *RecordLimitError
			if errors.As(err, &recordErr) {
				t.Fatalf("scan budget mislabeled record limit: %v", err)
			}
		})
	}
}

func TestScanFreezesEndBeforeVisitorAppend(t *testing.T) {
	complete := `{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	late := `{"type":"event_msg","payload":{"type":"turn_aborted"}}` + "\n"
	opts, path := fixture(t, "codex", codexMeta()+complete)
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var got []Event
	err = r.Scan(context.Background(), func(events []Event) error {
		got = append(got, events...)
		appendFile(t, path, late)
		return nil
	})
	if err != nil || len(got) != 1 || got[0].Kind != KindComplete {
		t.Fatalf("prefix=%+v, %v", got, err)
	}
	events, err := r.Poll(context.Background())
	if err != nil || len(events) != 1 || events[0].Kind != KindInterrupted {
		t.Fatalf("later append lost: %+v, %v", events, err)
	}
}

func TestScanPropagatesVisitorFailureAndCancellation(t *testing.T) {
	for _, cancelVisit := range []bool{false, true} {
		opts, _ := fixture(t, "codex", codexMeta())
		r, err := Open(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		visitorErr := errors.New("visitor rejected evidence")
		want := visitorErr
		if cancelVisit {
			want = context.Canceled
		}
		err = r.Scan(ctx, func([]Event) error {
			if cancelVisit {
				cancel()
				return nil
			}
			return visitorErr
		})
		cancel()
		r.Close()
		if !errors.Is(err, want) {
			t.Fatalf("scan returned success after visitor failure: %v", err)
		}
	}
}

func TestScanIncludesLateContradictionAfterIgnoredBatches(t *testing.T) {
	final := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","message":"answer"}}` + "\n"
	late := `{"type":"event_msg","payload":{"type":"turn_aborted"}}` + "\n"
	opts, _ := fixture(t, "codex", codexMeta()+final+strings.Repeat(`{"type":"ignored"}`+"\n", 100)+late)
	opts.MaxLineBytes, opts.MaxPollBytes = 128, 256
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var got []Event
	err = r.Scan(context.Background(), func(events []Event) error { got = append(got, events...); return nil })
	if err != nil || len(got) != 2 || got[0].Kind != KindFinal || got[1].Kind != KindInterrupted {
		t.Fatalf("incomplete accepted prefix: events=%+v, err=%v", got, err)
	}
}
