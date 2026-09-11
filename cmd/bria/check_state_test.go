package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCheckStateIsOfflineReadOnlyAndRejectsIncompatibleState(t *testing.T) {
	for _, document := range []string{"", `{"version":999,"private-content-sentinel":"secret"}`} {
		t.Run(document, func(t *testing.T) {
			configPath, statePath := writeStatusConfig(t, t.TempDir(), "unused-secret")
			if document != "" {
				if err := os.WriteFile(statePath, []byte(document), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, errOut strings.Builder
			code := runContextWithDependencies(context.Background(), []string{"check-state", "--config", configPath}, &out, &errOut, commandDependencies{})
			if document == "" {
				if code != 0 || out.String() != "Bria state compatibility: OK\nBria message journal compatibility: OK\n" {
					t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
				}
				if _, err := os.Stat(statePath); !os.IsNotExist(err) {
					t.Fatalf("read-only check created state: %v", err)
				}
			} else {
				if code != 1 || out.Len() != 0 || strings.Contains(errOut.String(), "private-content-sentinel") || strings.Contains(errOut.String(), "secret") {
					t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
				}
				got, err := os.ReadFile(statePath)
				if err != nil || string(got) != document {
					t.Fatalf("state changed: %v", err)
				}
			}
		})
	}
}

func TestCheckStatePreservesExistingReadOnlyFilePermissions(t *testing.T) {
	configPath, path := writeStatusConfig(t, t.TempDir(), "unused-secret")
	data := []byte(`{"version":1,"sessions":[]}`)
	if err := os.WriteFile(path, data, 0400); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	if code := runContextWithDependencies(context.Background(), []string{"check-state", "--config", configPath}, &out, &errOut, commandDependencies{}); code != 0 {
		t.Fatalf("code=%d error=%s", code, errOut.String())
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatalf("read-only probe changed permissions: info=%v err=%v", info, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("read-only probe changed content: %v", err)
	}
}

func TestCheckStateIncludesMessageJournalCompatibilityWithoutWritingIt(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		document string
		wantCode int
	}{
		{name: "current skipped phase", document: `{"version":3,"sessions":[{"session_id":"s","next_sequence":1,"inputs":[{"message_id":"old","sequence":1,"payload":"cHJpdmF0ZQ==","phase":"skipped","lease":{}}],"outputs":[],"input_recovery":{"token":"recovery","through_sequence":1,"phase":"committed"}}]}`, wantCode: 0},
		{name: "future incompatible", document: `{"version":999,"sessions":[],"private-content-sentinel":"secret"}`, wantCode: 1},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			configPath, statePath := writeStatusConfig(t, t.TempDir(), "unused-secret")
			journalPath := statePath + ".message-journal.json"
			if err := os.WriteFile(journalPath, []byte(fixture.document), 0o400); err != nil {
				t.Fatal(err)
			}
			var out, errOut strings.Builder
			code := runContextWithDependencies(context.Background(), []string{"check-state", "--config", configPath}, &out, &errOut, commandDependencies{})
			if code != fixture.wantCode || strings.Contains(errOut.String(), "private-content-sentinel") || strings.Contains(errOut.String(), "secret") {
				t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
			}
			got, err := os.ReadFile(journalPath)
			if err != nil || string(got) != fixture.document {
				t.Fatalf("journal changed: %v", err)
			}
			info, err := os.Stat(journalPath)
			if err != nil || info.Mode().Perm() != 0o400 {
				t.Fatalf("journal permissions changed: info=%v err=%v", info, err)
			}
		})
	}
}
