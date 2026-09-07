package nativetranscript

import (
	"context"
	"encoding/json"
	"testing"
)

func TestQuestionReaderPollIsBoundAndDoesNotReplay(t *testing.T) {
	body := `{"type":"user","sessionId":"` + testID + `","cwd":"/work","uuid":"turn1","message":{"content":"pick a target"}}` + "\n" +
		`{"type":"assistant","sessionId":"` + testID + `","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"question":"Which target?"}]}}]}}` + "\n"
	opts, _ := fixture(t, "claude", body)
	reader, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	events, err := reader.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	questions := 0
	for _, event := range events {
		if event.Kind == KindQuestion {
			questions++
			if event.SessionID != testID || event.TurnID != "turn1" || event.ID == "" {
				t.Fatalf("binding=%#v", event)
			}
		}
	}
	if questions != 1 {
		t.Fatalf("questions=%d events=%#v", questions, events)
	}
	if repeated, err := reader.Poll(context.Background()); err != nil || len(repeated) != 0 {
		t.Fatalf("replayed=%#v err=%v", repeated, err)
	}
}

func TestClaudeStructuredQuestionOnly(t *testing.T) {
	for _, test := range []struct {
		name, tool, input string
		want              bool
	}{
		{"question", "AskUserQuestion", `{"questions":[{"question":"Which target?","options":[{"label":"One"}]}]}`, true},
		{"permission", "Bash", `{"questions":[{"question":"Allow?"}]}`, false},
		{"lookalike", "askuserquestion", `{"questions":[{"question":"Allow?"}]}`, false},
		{"empty", "AskUserQuestion", `{"questions":[]}`, false},
		{"malformed", "AskUserQuestion", `{"questions":"target"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := parseState{turn: "turn1"}
			rec := map[string]any{"type": "assistant", "sessionId": "native1", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": test.tool, "input": json.RawMessage(test.input)}}}}
			line, _ := json.Marshal(rec)
			events, err := state.parse(line, Options{Provider: "claude", SessionID: "native1"}, 10)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range events {
				if event.Kind == KindQuestion {
					count++
					if event.Text != "Which target?" || event.TurnID != "turn1" || event.SessionID != "native1" {
						t.Fatalf("wrong question binding: %#v", event)
					}
				}
			}
			if (count == 1) != test.want || count > 1 {
				t.Fatalf("questions=%d want=%v events=%#v", count, test.want, events)
			}
		})
	}
}
