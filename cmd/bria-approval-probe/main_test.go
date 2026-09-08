package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestChangedPaneIdentityCannotReceiveApprovalKey(t *testing.T) {
	identity, sent := "$1:@2:%3:400:0", false
	pane := &terminal{target: "%3", identity: identity}
	pane.execute = func(_ context.Context, args ...string) (string, error) {
		switch args[0] {
		case "display-message":
			return identity, nil
		case "capture-pane":
			return "screen", nil
		case "send-keys":
			sent = true
		}
		return "", nil
	}
	if _, err := pane.Capture(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity = "$1:@2:%3:401:0"
	if err := pane.Key(context.Background(), "Enter"); err == nil || sent {
		t.Fatal("changed provider process received Enter")
	}
}

func TestDurableClaimAllowsOneConcurrentDecisionAndNoReplay(t *testing.T) {
	dir := t.TempDir()
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if claimOnce(dir, "session:pane:pid", strings.Repeat("a", 64)) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("decisions = %d, want one", successes.Load())
	}
	if err := claimOnce(dir, "session:pane:pid", strings.Repeat("a", 64)); err == nil {
		t.Fatal("replayed authorization")
	}
	if err := claimOnce(dir, "different:pane:pid", strings.Repeat("a", 64)); err != nil {
		t.Fatal("different process was incorrectly consumed", err)
	}
}
