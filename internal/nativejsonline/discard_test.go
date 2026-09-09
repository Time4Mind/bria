package nativejsonline_test

import (
	"reflect"
	"strings"
	"testing"

	"bria/internal/nativejsonline"
)

func TestDiscardIndexTracksExactCandidateStringsAndClonesIndependently(t *testing.T) {
	line := &nativejsonline.Line{Max: 256}
	push(t, line, `{"keep":"","output":"`+strings.Repeat("x", 600)+`","id":"exact","more":"`)
	clone := line.Clone()
	push(t, clone, strings.Repeat("y", 600)+`"}`)
	if done, err := clone.Push('\n'); !done || err != nil {
		t.Fatalf("completed clone: done=%v err=%v", done, err)
	}
	want := `{"keep":"","output":"","id":"exact","more":""}`
	if string(clone.Bytes()) != want || !reflect.DeepEqual(clone.Discarded, []int{20, 43}) {
		t.Fatalf("discard positions/candidate: %q %v", clone.Bytes(), clone.Discarded)
	}
	clone.Discarded[0] = -1
	if !reflect.DeepEqual(line.Discarded, []int{20}) {
		t.Fatal("clone mutated prior discard checkpoint")
	}
}
