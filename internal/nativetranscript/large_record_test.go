package nativetranscript

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestRecordLimitSafeDiagnostic(t *testing.T) {
	opts, _ := fixture(t, "codex", codexMeta()+strings.Repeat("x", 200)+"\n")
	opts.MaxLineBytes, opts.MaxPollBytes = 128, 512
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = r.Poll(context.Background())
	if !errors.Is(err, ErrLimit) || err.Error() != "native transcript record exceeds limit (offset=94 observed=129 limit=128)" {
		t.Fatalf("safe record diagnostic: %v", err)
	}
	var recordErr *RecordLimitError
	if !errors.As(err, &recordErr) || recordErr.Offset != 94 || recordErr.Observed != 129 || recordErr.Limit != 128 {
		t.Fatalf("record limit fields: %+v", recordErr)
	}
}

func TestRecordLimitDistinguishesHeaderOptionsAndDrainBudget(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		opts, _ := fixture(t, "codex", `{"type":"session_meta","padding":"`+strings.Repeat("x", 500)+`"}`+"\n")
		opts.MaxLineBytes, opts.MaxPollBytes = 128, 256
		_, err := Open(context.Background(), opts)
		var recordErr *RecordLimitError
		if !errors.Is(err, ErrLimit) || !errors.As(err, &recordErr) || recordErr.Offset != 0 || recordErr.Observed != 256 {
			t.Fatalf("header error=%v, fields=%+v", err, recordErr)
		}
	})
	t.Run("options", func(t *testing.T) {
		opts, _ := fixture(t, "codex", codexMeta())
		opts.MaxLineBytes = 127
		_, err := Open(context.Background(), opts)
		var recordErr *RecordLimitError
		if !errors.Is(err, ErrLimit) || errors.As(err, &recordErr) {
			t.Fatalf("options classified as record: %v", err)
		}
	})
	t.Run("drain passes", func(t *testing.T) {
		opts, _ := fixture(t, "codex", codexMeta()+strings.Repeat(`{"type":"ignored"}`+"\n", 3000))
		opts.MaxLineBytes, opts.MaxPollBytes = 128, 256
		r, err := Open(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		err = r.Drain(context.Background())
		var recordErr *RecordLimitError
		if !errors.Is(err, ErrLimit) || errors.As(err, &recordErr) {
			t.Fatalf("drain budget classified as record: %v", err)
		}
	})
}

func TestLargeRecordFailsClosed(t *testing.T) {
	large := strings.Repeat("x", 2<<20)
	key := strings.Repeat("k", 9000)
	for _, test := range []struct {
		name, record string
		want         error
	}{
		{"unknown", `{"type":"event_msg","payload":{"type":"unknown","text":"` + large + `"}}`, ErrLimit},
		{"significant final", `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","message":"` + large + `"}}`, ErrLimit},
		{"syntax", `{"type":"event_msg","payload":{"type":"item_completed","item":{"stdout":"` + large + `",}}}`, ErrMalformed},
		{"duplicate discriminator", `{"type":"event_msg","payload":{"type":"agent_message","type":"item_completed","text":"` + large + `"}}`, ErrMalformed},
		{"escaped duplicate", `{"type":"event_msg","payload":{"type":"item_completed","item":{"key":1,"\u006bey":2,"text":"` + large + `"}}}`, ErrMalformed},
		{"long duplicate", `{"type":"event_msg","payload":{"type":"item_completed","item":{"` + key + `":1,"` + key + `":2,"text":"` + large + `"}}}`, ErrMalformed},
		{"invalid dropped UTF8", `{"type":"event_msg","payload":{"type":"item_completed","item":{"text":"` + large + "\xff" + `"}}}`, ErrLimit},
		{"invalid dropped escape", `{"type":"event_msg","payload":{"type":"item_completed","item":{"text":"` + large + `\q"}}}`, ErrLimit},
		{"invalid dropped unicode escape", `{"type":"event_msg","payload":{"type":"item_completed","item":{"text":"` + large + `\u00xz"}}}`, ErrLimit},
		{"unclosed dropped string", `{"type":"event_msg","payload":{"type":"item_completed","item":{"text":"` + large, ErrLimit},
		{"structure budget", `{"type":"event_msg","payload":{"type":"item_completed","item":[` + strings.Repeat(`"x",`, 300000) + `"x"]}}`, ErrLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, _ := fixture(t, "codex", codexMeta()+test.record+"\n"+`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n")
			r, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			for retry := 0; retry < 2; retry++ {
				events, err := r.Poll(context.Background())
				if len(events) != 0 || !errors.Is(err, test.want) {
					t.Fatalf("attempt=%d events=%+v err=%v want=%v", retry, events, err, test.want)
				}
			}
		})
	}
}

func TestLargeRecordPartialUTF8DrainAndFollowingInteraction(t *testing.T) {
	prefix := `{"type":"event_msg","payload":{"type":"item_completed","item":{"text":"` + strings.Repeat("x", 5<<20)
	for _, drain := range []bool{false, true} {
		t.Run(strconv.FormatBool(drain), func(t *testing.T) {
			opts, path := fixture(t, "codex", codexMeta()+prefix+"\xf0\x9f")
			r, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			for attempt := 0; attempt < 3; attempt++ {
				if drain {
					if err := r.Drain(context.Background()); !errors.Is(err, ErrNotFound) {
						t.Fatalf("partial drain=%v", err)
					}
				} else if events, err := r.Poll(context.Background()); err != nil || len(events) != 0 {
					t.Fatalf("partial poll=%v, %v", events, err)
				}
			}
			closing := "\x98\x80" + `"}}}` + "\n"
			appendFile(t, path, closing)
			if drain {
				if err := r.Drain(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			interaction := `{"type":"response_item","payload":{"type":"function_call","call_id":"approval1","name":"request_user_input","arguments":"{\"question\":\"Proceed?\"}"}}` + "\n"
			final := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","message":"done"}}` + "\n"
			appendFile(t, path, interaction+final)
			events, err := r.Poll(context.Background())
			if err != nil || len(events) != 2 || events[0].Kind != KindTool || events[0].Metadata == nil || events[0].Metadata.Name != "request_user_input" || !toolTextEquals(events[0].Metadata.Arguments, `{"question":"Proceed?"}`) || events[1].Kind != KindFinal || events[1].Text != "done" {
				t.Fatalf("following events=%+v, err=%v", events, err)
			}
			wantOffset := len(codexMeta()) + len(prefix) + 2 + len(closing)
			if events[0].ID != strconv.Itoa(wantOffset)+":0" || events[1].ID != strconv.Itoa(wantOffset+len(interaction))+":0" {
				t.Fatalf("physical event offsets=%+v", events)
			}
			if events, err := r.Poll(context.Background()); err != nil || len(events) != 0 {
				t.Fatalf("replay=%+v, %v", events, err)
			}
		})
	}
}

func TestLargeDiagnosticRecordPreservesFollowingFinal(t *testing.T) {
	large := `{"type":"event_msg","payload":{"type":"item_completed","item":{"stdout":"` + strings.Repeat("x", 2<<20) + `"}}}` + "\n"
	opts, _ := fixture(t, "codex", codexMeta()+large+`{"type":"event_msg","payload":{"type":"agent_message","message":"finished","phase":"final","turn_id":"turn1"}}`+"\n"+`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn1"}}`+"\n")
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events, err := r.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != KindFinal || events[0].Text != "finished" || events[1].Kind != KindComplete {
		t.Fatalf("events=%+v", events)
	}
	events, err = r.Poll(context.Background())
	if err != nil || len(events) != 0 {
		t.Fatalf("replay count=%d error=%v", len(events), err)
	}
}

func TestLargeDiagnosticDistinctLongKeys(t *testing.T) {
	key := strings.Repeat("k", 9000)
	large := `{"type":"event_msg","payload":{"type":"item_completed","item":{"` + key + `1":"a","` + key + `2":"b","stdout":"` + strings.Repeat("x", 2<<20) + `"}}}` + "\n"
	opts, _ := fixture(t, "codex", codexMeta()+large+`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n")
	r, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events, err := r.Poll(context.Background())
	if err != nil || len(events) != 1 || events[0].Kind != KindComplete {
		t.Fatalf("distinct keys rejected: events=%+v, err=%v", events, err)
	}
}
