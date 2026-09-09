//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var errNestedHeartbeatActive = errors.New("nested heartbeat still changing")
var errNestedHeartbeatInvalid = errors.New("invalid nested heartbeat fixture")

func publishNestedHeartbeat(path string, counter uint64) error {
	if counter == 0 {
		return errNestedHeartbeatInvalid
	}
	// One helper owns this path. A kill may leave a candidate, but cannot
	// truncate the last fully published heartbeat observed by the parent.
	candidate := path + ".candidate"
	if err := os.WriteFile(candidate, []byte(strconv.FormatUint(counter, 10)), 0600); err != nil {
		return err
	}
	return os.Rename(candidate, path)
}

func readNestedHeartbeat(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	counter, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil || counter == 0 || strconv.FormatUint(counter, 10) != string(data) {
		return 0, errNestedHeartbeatInvalid
	}
	return counter, nil
}

func waitNestedHeartbeatIncrease(path string, prior uint64, timeout time.Duration) (uint64, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		counter, err := readNestedHeartbeat(path)
		if err == nil {
			if counter > prior {
				return counter, nil
			}
			if counter < prior {
				return 0, errNestedHeartbeatInvalid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		select {
		case <-deadline.C:
			return 0, errors.New("nested heartbeat did not increase")
		case <-ticker.C:
		}
	}
}

func waitNestedHeartbeatQuiet(path string, stableFor, timeout time.Duration) error {
	return waitNestedHeartbeatQuietSamples(func() (uint64, error) {
		return readNestedHeartbeat(path)
	}, stableFor, timeout)
}

func waitNestedHeartbeatQuietSamples(read func() (uint64, error), stableFor, timeout time.Duration) error {
	prior, err := read()
	if err != nil {
		return err
	}
	stableSince := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			return errNestedHeartbeatActive
		case <-ticker.C:
			current, err := read()
			if err != nil {
				return err
			}
			if current < prior {
				return errNestedHeartbeatInvalid
			}
			if current != prior {
				prior, stableSince = current, time.Now()
			} else if time.Since(stableSince) >= stableFor {
				return nil
			}
		}
	}
}

func TestNestedHeartbeatPublicationReplacesWholeCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beat")
	if err := publishNestedHeartbeat(path, 1); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	prior, err := old.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := publishNestedHeartbeat(path, 200); err != nil {
		t.Fatal(err)
	}
	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(prior, current) {
		t.Fatal("heartbeat publication truncates the reader-visible inode")
	}
	data := make([]byte, 8)
	n, err := old.Read(data)
	if err != nil || string(data[:n]) != "1" {
		t.Fatal("published counter changed through an already-open reader")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "200" {
		t.Fatal("new heartbeat was not published as one complete counter")
	}
}

func TestNestedHeartbeatRejectsInvalidQuiescenceEvidence(t *testing.T) {
	for _, value := range []string{"", "not-a-counter", "0", "-1", "1\n", "18446744073709551616"} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "beat")
			if err := os.WriteFile(path, []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			if err := waitNestedHeartbeatQuiet(path, time.Millisecond, time.Second); !errors.Is(err, errNestedHeartbeatInvalid) {
				t.Fatalf("invalid fixture accepted as stable: %v", err)
			}
		})
	}
}

func TestNestedHeartbeatAdvancingSamplesNeverPassQuiescence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beat")
	var counter uint64
	// Publish before each sample: a ticker on a different goroutine cannot
	// guarantee progress under CI scheduling delays. Keep real atomic file IO,
	// but make the negative control's strictly advancing evidence explicit.
	read := func() (uint64, error) {
		counter++
		if err := publishNestedHeartbeat(path, counter); err != nil {
			return 0, err
		}
		return readNestedHeartbeat(path)
	}
	started := time.Now()
	err := waitNestedHeartbeatQuietSamples(read, 100*time.Millisecond, 200*time.Millisecond)
	if !errors.Is(err, errNestedHeartbeatActive) || time.Since(started) < 200*time.Millisecond {
		t.Fatalf("advancing heartbeat quiescence=%v elapsed=%v; want bounded timeout, never success", err, time.Since(started))
	}
}
