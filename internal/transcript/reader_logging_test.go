package transcript

import (
	"errors"
	"testing"
	"time"
)

func TestTranscriptReadLoggingSuppressesOnlyFastSuccessfulCacheHits(t *testing.T) {
	if shouldLogTranscriptRead(true, time.Millisecond, nil) {
		t.Fatal("fast successful cache hit was logged")
	}
	for name, input := range map[string]struct {
		cacheHit bool
		duration time.Duration
		err      error
	}{
		"append":       {cacheHit: false, duration: time.Millisecond},
		"slow cache":   {cacheHit: true, duration: tracedTranscriptRead},
		"failed cache": {cacheHit: true, duration: time.Millisecond, err: errors.New("read")},
	} {
		if !shouldLogTranscriptRead(input.cacheHit, input.duration, input.err) {
			t.Fatalf("%s read was suppressed", name)
		}
	}
}
