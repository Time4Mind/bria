package nativeadapter

import (
	"strings"
	"testing"

	"bria/internal/runtimeprotocol"
)

func TestNativeApprovalControlTraversesRealTerminalAndChecksReceipt(t *testing.T) {
	h := startNativeFixture(t)
	if h.receive(t).Type != runtimeprotocol.TypeReady {
		t.Fatal("missing readiness")
	}
	for _, command := range []string{"/approval-full", "/approval-collapsed"} {
		h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeNativeControl, RequestID: "open", Command: command})
		pending := h.receive(t)
		if !pending.Interactive || !strings.Contains(pending.Text, "Yes, proceed") {
			t.Fatal("pending native approval missing")
		}
		h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeNativeControl, RequestID: "yes", Key: "approve_once", ExpectedHash: pending.Hash})
		receipt := h.receive(t)
		if receipt.ErrorCode != "" || receipt.Interactive || !strings.Contains(receipt.FullText, "✔ You approved codex to run fixture this time") {
			t.Fatalf("approval receipt not confirmed: %+v", receipt)
		}
		h.send(t, runtimeprotocol.ParentMessage{Type: runtimeprotocol.TypeNativeControl, RequestID: "replay", Key: "approve_once", ExpectedHash: pending.Hash})
		if h.receive(t).ErrorCode != "stale" {
			t.Fatal("stale approval replay accepted")
		}
	}
}
