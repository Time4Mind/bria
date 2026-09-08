package notificationstate_test

import (
	"os"
	"path/filepath"
	"testing"

	"bria/internal/notificationstate"
)

func TestOpenRejectsMalformedLegacyPartIdentity(t *testing.T) {
	for _, partID := range []string{"op:part:0-of-1", "op:part:2-of-1", "op:part:1-of-0", "op:part:01-of-2", "op:part:1-of-2:extra"} {
		t.Run(partID, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parts.json")
			if err := os.WriteFile(path, []byte(`{"version":1,"operations":{"op":{"`+partID+`":{"state":"unknown"}}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := notificationstate.OpenFilePartReceiptStore(path); err == nil {
				t.Fatal("malformed durable part identity accepted")
			}
		})
	}
}
