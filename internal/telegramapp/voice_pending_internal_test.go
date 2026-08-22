package telegramapp

import (
	"fmt"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestVoiceConfirmationCacheIsIdempotentAndBounded(t *testing.T) {
	state := newVoicePendingState()
	key := voicePendingKey{
		userID: 7, generation: 1,
		ref: domain.SessionRef{NodeID: "node", SessionID: "session"},
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
	state.sweepVoiceConfirmationsLocked(now.Add(24 * time.Hour))
	if len(state.confirmedVoiceOrder) != maxVoiceConfirmations ||
		len(state.confirmedVoices[key]) != maxVoiceConfirmations {
		state.voiceMu.Unlock()
		t.Fatal("bounded voice confirmations changed only because time elapsed")
	}
	state.voiceMu.Unlock()
}

func TestVoiceBaselineDropsStaleTranscriptOnGenerationChange(t *testing.T) {
	baseline := voicePendingBaseline{
		ref:        domain.SessionRef{NodeID: "node", SessionID: "session"},
		generation: 1, lastUserEvent: "old", baselineID: "old-id",
		userEventCount: 7, ordinal: 4, known: true,
	}
	now := time.Unix(2000, 0).UTC()
	resetVoiceBaselineGeneration(&baseline, 2, now)
	if baseline.generation != 2 || baseline.receivedAt != now || baseline.known ||
		baseline.lastUserEvent != "" || baseline.baselineID != "" ||
		baseline.userEventCount != 0 || baseline.ordinal != 1 || len(baseline.events) != 0 {
		t.Fatalf("reset baseline=%#v", baseline)
	}
}
