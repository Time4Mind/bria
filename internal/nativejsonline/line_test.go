package nativejsonline_test

import (
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/nativejsonline"
)

func push(t *testing.T, line *nativejsonline.Line, text string) {
	t.Helper()
	for i := 0; i < len(text); i++ {
		if done, err := line.Push(text[i]); err != nil || done {
			t.Fatalf("unexpected boundary at %d: done=%v err=%v", i, done, err)
		}
	}
}

func TestLineRetainsSmallRecordsExactly(t *testing.T) {
	for _, text := range []string{
		`{"key":"` + strings.Repeat("x", 9000) + `"}`,
		`{"ключ":"🙂é界","escaped":"\uD83D\uDE00\n\t\"\\"}`,
		`["` + strings.Repeat("x", 9000) + `",{"key":"value"}]`,
	} {
		line := &nativejsonline.Line{Max: len(text)}
		push(t, line, text)
		if done, err := line.Push('\n'); err != nil || !done || line.Large || string(line.Bytes()) != text {
			t.Fatalf("record changed at exact limit: done=%v err=%v large=%v", done, err, line.Large)
		}
	}
}

func TestLineLargeValueUsesBoundedAllocations(t *testing.T) {
	// Input is produced one byte at a time; the measurement includes all of the
	// framer's allocations, not a preallocated giant test fixture or a spool.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	line := &nativejsonline.Line{Max: 4096}
	push(t, line, `{"text":"`)
	for i := 0; i < 16<<20; i++ {
		if done, err := line.Push('x'); done || err != nil {
			t.Fatalf("large value byte=%d done=%v err=%v", i, done, err)
		}
	}
	push(t, line, `"}`)
	done, err := line.Push('\n')
	runtime.ReadMemStats(&after)
	if err != nil || !done || !line.Large || string(line.Bytes()) != `{"text":""}` {
		t.Fatalf("large candidate=%q done=%v err=%v", line.Bytes(), done, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("16MiB input allocated %d bytes (budget 1MiB)", allocated)
	}
}

func TestLineClonePreservesIncompleteUTF8AndEscapes(t *testing.T) {
	for _, fragment := range []string{"\xf0\x9f", `\uD8`, `\`} {
		line := &nativejsonline.Line{Max: 256}
		push(t, line, `{"text":"`+strings.Repeat("x", 1000)+fragment)
		cloned := line.Clone()
		suffix := map[string]string{"\xf0\x9f": "\x98\x80", `\uD8`: `3D\uDE00`, `\`: `n`}[fragment]
		push(t, cloned, suffix+`"}`)
		if done, err := cloned.Push('\n'); !done || err != nil || !json.Valid(cloned.Bytes()) || !utf8.Valid(cloned.Bytes()) {
			t.Fatalf("clone completion done=%v err=%v bytes=%q", done, err, cloned.Bytes())
		}
		// Finishing the clone must not finish or corrupt the original checkpoint.
		if _, err := line.Push('\n'); !errors.Is(err, nativejsonline.ErrMalformed) {
			t.Fatalf("original incomplete string accepted: %v", err)
		}
	}
}
