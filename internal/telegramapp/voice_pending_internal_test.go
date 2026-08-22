package telegramapp

import (
	"fmt"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestVoiceConfirmationCacheIsIdempotentBoundedAndExpires(t *testing.T) {
	state := newVoicePendingState()
	key := voicePendingKey{
		userID: 7,
		ref:    domain.SessionRef{NodeID: "node", SessionID: "session"},
	}
	now := time.Unix(1000, 0).UTC()
	state.voiceMu.Lock()
	if !state.rememberVoiceConfirmationLocked(key, "voice-0", now) ||
		state.rememberVoiceConfirmationLocked(key, "voice-0", now.Add(time.Second)) {
		state.voiceMu.Unlock()
		t.Fatal("voice confirmation cache is not idempotent")
	}
	for index := 1; index <= maxVoiceConfirmations+10; index++ {
		state.rememberVoiceConfirmationLocked(
			key, fmt.Sprintf("voice-%d", index), now.Add(time.Duration(index)*time.Millisecond),
		)
	}
	if len(state.confirmedVoiceOrder) != maxVoiceConfirmations ||
		len(state.confirmedVoices[key]) != maxVoiceConfirmations {
		state.voiceMu.Unlock()
		t.Fatalf("confirmation cache order=%d entries=%d",
			len(state.confirmedVoiceOrder), len(state.confirmedVoices[key]))
	}
	state.sweepVoiceConfirmationsLocked(now.Add(voicePendingLifetime + time.Minute))
	if len(state.confirmedVoiceOrder) != 0 || len(state.confirmedVoices) != 0 {
		state.voiceMu.Unlock()
		t.Fatal("expired voice confirmations survived sweep")
	}
	state.voiceMu.Unlock()
}
