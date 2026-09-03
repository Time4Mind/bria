package telegramrecovery_test

import (
	"testing"

	"bria/internal/telegramrecovery"
	"bria/internal/telegramui"
)

func TestProjectUnknownBindsActionsToPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase     string
		want      telegramui.Action
		wantCount int
	}{
		{phase: telegramrecovery.EffectUnknownPhase, want: telegramui.ActionCallbackEffectConfirmed, wantCount: 2},
		{phase: telegramrecovery.EffectRetryUnknownPhase, want: telegramui.ActionCallbackEffectConfirmed, wantCount: 1},
		{phase: telegramrecovery.SendUnknownPhase, want: telegramui.ActionCallbackSendConfirmed, wantCount: 2},
	}
	for _, test := range tests {
		text, keyboard, err := telegramrecovery.ProjectUnknown(test.phase, "session-1", "callback:1")
		if err != nil {
			t.Fatalf("project %s: %v", test.phase, err)
		}
		if text == "" || len(keyboard.Rows) != 1 || len(keyboard.Rows[0]) != test.wantCount {
			t.Fatalf("invalid recovery projection for %s: text=%q keyboard=%+v", test.phase, text, keyboard)
		}
		if got := keyboard.Rows[0][0].Action; got != test.want {
			t.Fatalf("first action for %s = %q, want %q", test.phase, got, test.want)
		}
	}
}

func TestProjectUnknownRejectsCommittedPhase(t *testing.T) {
	t.Parallel()

	if _, _, err := telegramrecovery.ProjectUnknown("committed", "session-1", "callback:1"); err == nil {
		t.Fatal("expected unsupported phase error")
	}
}
