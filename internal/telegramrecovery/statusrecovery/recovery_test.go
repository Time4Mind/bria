package statusrecovery_test

import (
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramrecovery/statusrecovery"
	"bria/internal/telegramstate"
)

func TestValidRequiresExactSequenceCarrierAndSessionOrGlobalScope(t *testing.T) {
	valid := statusrecovery.Binding{OperationID: "status:9", UpdateID: 9,
		Scope:   statusrecovery.Scope{Kind: statusrecovery.ScopeSession, SessionID: domain.SessionID("session-9")},
		Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Sequence: 9}
	if !statusrecovery.Valid(valid) {
		t.Fatal("valid session binding rejected")
	}
	global := valid
	global.Scope = statusrecovery.Scope{Kind: statusrecovery.ScopeGlobal}
	if !statusrecovery.Valid(global) {
		t.Fatal("valid global binding rejected")
	}
	for _, mutate := range []func(*statusrecovery.Binding){
		func(binding *statusrecovery.Binding) { binding.UpdateID++ },
		func(binding *statusrecovery.Binding) { binding.Sequence++ },
		func(binding *statusrecovery.Binding) { binding.Carrier.MessageID = 0 },
		func(binding *statusrecovery.Binding) { binding.Scope.SessionID = "" },
	} {
		invalid := valid
		mutate(&invalid)
		if statusrecovery.Valid(invalid) {
			t.Fatalf("invalid binding accepted: %#v", invalid)
		}
	}
}
