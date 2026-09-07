package nativeadapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeReceiptsRoundTripBoundToExactSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	a := &adapter{config: Config{StateDir: dir}, id: fixtureSession, receipts: map[string]string{"message-1": "completed", "message-2": "unknown"}}
	if err := a.saveReceipts(); err != nil {
		t.Fatal(err)
	}
	path, err := a.receiptPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		path string
		mode os.FileMode
	}{{dir, 0700}, {path, 0600}} {
		info, err := os.Stat(target.path)
		if err != nil || info.Mode().Perm() != target.mode {
			t.Fatal("receipt persistence permissions mismatch")
		}
	}
	b := &adapter{config: Config{StateDir: dir}, id: fixtureSession}
	if err := b.loadReceipts(); err != nil || b.receipts["message-1"] != "completed" || b.receipts["message-2"] != "unknown" {
		t.Fatal("receipt roundtrip failed")
	}
	// A correctly named file cannot substitute a different provider identity.
	if err := os.WriteFile(path, []byte(`{"session_id":"foreign","receipts":{"message-1":"completed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.loadReceipts(); err == nil {
		t.Fatal("foreign receipt binding accepted")
	}
}

func TestNativeReceiptMalformedAndCapacityBounds(t *testing.T) {
	for _, document := range []string{
		`{"session_id":"` + fixtureSession + `","receipts":{"":"completed"}}`,
		`{"session_id":"` + fixtureSession + `","receipts":{"m":"arbitrary"}}`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		a := &adapter{config: Config{StateDir: t.TempDir()}, id: fixtureSession}
		path, err := a.receiptPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
		if err := a.loadReceipts(); err == nil {
			t.Fatal("malformed or oversized receipt document accepted")
		}
	}
	for _, a := range []*adapter{
		{config: Config{StateDir: "relative"}, id: fixtureSession},
		{config: Config{StateDir: t.TempDir()}, id: "../foreign"},
	} {
		if _, err := a.receiptPath(); err == nil {
			t.Fatal("unbounded receipt path accepted")
		}
	}
}
