package consensus

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRaftLogRateLimiterSuppressesOnlyRepeatedConnectivityNoise(t *testing.T) {
	var output bytes.Buffer
	now := time.Unix(100, 0)
	writer := newRaftLogRateLimiter(
		&output,
		30*time.Second,
		func() time.Time { return now },
	)
	first := "[ERROR] raft: failed to heartbeat to: peer=node-2\n"
	if _, err := writer.Write([]byte(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(first)); err != nil {
		t.Fatal(err)
	}
	important := "[ERROR] raft: failed to contact quorum, stepping down\n"
	if _, err := writer.Write([]byte(important)); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); strings.Count(got, first) != 1 || !strings.Contains(got, important) {
		t.Fatalf("unexpected initial output %q", got)
	}
	now = now.Add(31 * time.Second)
	if _, err := writer.Write([]byte(first)); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.Count(got, first) != 2 ||
		!strings.Contains(got, "suppressed 1 repeated heartbeat failure messages") {
		t.Fatalf("unexpected released output %q", got)
	}
}

func TestRaftLogRateLimiterKeepsFailureVisible(t *testing.T) {
	want := errors.New("write failed")
	writer := newRaftLogRateLimiter(
		failingLogWriter{err: want}, time.Minute, time.Now,
	)
	if _, err := writer.Write([]byte("failed to heartbeat to peer")); !errors.Is(err, want) {
		t.Fatalf("error=%v, want %v", err, want)
	}
}

type failingLogWriter struct{ err error }

func (w failingLogWriter) Write([]byte) (int, error) { return 0, w.err }
