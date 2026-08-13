package consensus

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type raftLogWindow struct {
	last       time.Time
	suppressed uint64
}

type raftLogRateLimiter struct {
	output io.Writer
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]raftLogWindow
}

func newRaftLogRateLimiter(
	output io.Writer,
	window time.Duration,
	now func() time.Time,
) io.Writer {
	return &raftLogRateLimiter{
		output:  output,
		window:  window,
		now:     now,
		windows: map[string]raftLogWindow{},
	}
}

func (w *raftLogRateLimiter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	class := repetitiveRaftLogClass(string(payload))
	if class == "" {
		return w.output.Write(payload)
	}
	now := w.now()
	entry := w.windows[class]
	if !entry.last.IsZero() && now.Sub(entry.last) < w.window {
		entry.suppressed++
		w.windows[class] = entry
		return len(payload), nil
	}
	if entry.suppressed > 0 {
		summary := fmt.Sprintf(
			"[WARN] raft: suppressed %d repeated %s messages\n",
			entry.suppressed,
			class,
		)
		if _, err := io.WriteString(w.output, summary); err != nil {
			return 0, err
		}
	}
	w.windows[class] = raftLogWindow{last: now}
	return w.output.Write(payload)
}

func repetitiveRaftLogClass(message string) string {
	lower := strings.ToLower(message)
	patterns := []struct {
		text  string
		class string
	}{
		{"failed to heartbeat to", "heartbeat failure"},
		{"failed to appendentries to", "append failure"},
		{"failed to make requestvote rpc", "vote RPC failure"},
		{"heartbeat timeout reached", "heartbeat timeout"},
		{"election timeout reached, restarting election", "election retry"},
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern.text) {
			return pattern.class
		}
	}
	return ""
}
