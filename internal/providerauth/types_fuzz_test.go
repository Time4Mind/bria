package providerauth

import "testing"

func FuzzFlowIDValidation(f *testing.F) {
	for _, seed := range []string{
		"abcdefghijklmnopqrstuvwx", "", "../../credentials", "id with space",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		valid := validFlowID(value)
		if valid && (len(value) < 20 || len(value) > 64) {
			t.Fatalf("accepted invalid length %d", len(value))
		}
	})
}

func FuzzStartRequestValidation(f *testing.F) {
	f.Add(int64(7), "node", "claude")
	f.Add(int64(0), "", "../../codex")
	f.Fuzz(func(t *testing.T, actor int64, nodeID, backend string) {
		request, err := normalizeStart(StartRequest{
			ActorID: actor, NodeID: nodeID, Backend: backend,
		})
		if err == nil && (request.ActorID <= 0 || request.NodeID == "" ||
			(request.Backend != BackendClaude && request.Backend != BackendCodex)) {
			t.Fatalf("accepted invalid request: %+v", request)
		}
	})
}
