package promptpreprocess

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type rollbackV1Envelope struct {
	Instruction string `json:"instruction"`
	Text        string `json:"text"`
	Processed   string `json:"processed,omitempty"`
	Prepared    bool   `json:"prepared,omitempty"`
	Failed      bool   `json:"failed,omitempty"`
}

func decodeRollbackV1(t *testing.T, payload []byte) rollbackV1Envelope {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload[len(envelopePrefix):]))
	decoder.DisallowUnknownFields()
	var value rollbackV1Envelope
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("previous v1 decoder rejected payload: %v", err)
	}
	return value
}

func TestDurableEnvelopePreservesSatelliteMode(t *testing.T) {
	for _, mode := range []Mode{ModeShared, ModePerSession} {
		t.Run(string(mode), func(t *testing.T) {
			payload, err := EncodeMode(mode, "clean", "raw text")
			if err != nil {
				t.Fatal(err)
			}
			state, err := DecodeState(payload)
			if err != nil || state.Mode != mode || !state.Enabled {
				t.Fatalf("DecodeState() = %#v, %v", state, err)
			}
			prepared, err := MarkPrepared(payload, "clean text", false)
			if err != nil {
				t.Fatal(err)
			}
			state, err = DecodeState(prepared)
			if err != nil || state.Mode != mode || !state.Prepared {
				t.Fatalf("prepared DecodeState() = %#v, %v", state, err)
			}
		})
	}
}

func TestSatelliteModeEnvelopeRemainsReadableByPreviousStrictV1Decoder(t *testing.T) {
	payload, err := EncodeMode(ModePerSession, "clean", "raw text")
	if err != nil {
		t.Fatal(err)
	}
	legacy := decodeRollbackV1(t, payload)
	if legacy.Text != "raw text" || !strings.Contains(legacy.Instruction, "satellite-mode=per_session") {
		t.Fatalf("legacy envelope = %#v", legacy)
	}
	prepared, err := MarkPrepared(payload, "clean text", false)
	if err != nil {
		t.Fatal(err)
	}
	legacy = decodeRollbackV1(t, prepared)
	if !legacy.Prepared || legacy.Processed != "clean text" {
		t.Fatalf("legacy prepared envelope = %#v", legacy)
	}
}

func TestLegacyEnvelopeDefaultsToSharedAndRawPayloadIsDisabled(t *testing.T) {
	legacyJSON, err := json.Marshal(map[string]string{"instruction": "clean", "text": "raw text"})
	if err != nil {
		t.Fatal(err)
	}
	legacy := append([]byte(envelopePrefix), legacyJSON...)
	state, err := DecodeState(legacy)
	if err != nil || state.Mode != ModeShared || !state.Enabled {
		t.Fatalf("legacy DecodeState() = %#v, %v", state, err)
	}
	state, err = DecodeState([]byte("raw text"))
	if err != nil || state.Mode != ModeDisabled || state.Enabled || state.Original != "raw text" {
		t.Fatalf("raw DecodeState() = %#v, %v", state, err)
	}
}

func TestEncodeCompatibilityUsesSharedAndDisabledModeStaysRaw(t *testing.T) {
	payload, err := Encode("clean", "raw text")
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(payload)
	if err != nil || state.Mode != ModeShared {
		t.Fatalf("Encode mode = %q, %v", state.Mode, err)
	}
	payload, err = EncodeMode(ModeDisabled, "clean", "raw text")
	if err != nil || string(payload) != "raw text" {
		t.Fatalf("disabled payload = %q, %v", payload, err)
	}
	if _, err := EncodeMode(Mode("unknown"), "clean", "raw text"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestEncodeUsesExactSpeechCleanupDefaultAndPreservesCustomInstruction(t *testing.T) {
	wantDefault := "Преобразуй распознанную речь в короткий и понятный запрос. Удали нецензурную лексику, слова-паразиты, повторы и не относящийся к задаче речевой мусор. Исправь грамматику и только очевидные ошибки распознавания. Сохрани исходное намерение, все существенные условия, отрицания, имена, числа, даты и команды. Ничего не додумывай. Если исправление неоднозначно, оставь сомнительный фрагмент максимально близко к оригиналу. Верни только готовый запрос."
	for _, test := range []struct{ name, configured, want string }{
		{"default", "", wantDefault},
		{"blank", " \n\t", wantDefault},
		{"custom", "Сохрани текст дословно.", "Сохрани текст дословно."},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := Encode(test.configured, "Не запускай команду 12 сентября.")
			if err != nil {
				t.Fatal(err)
			}
			instruction, original, enabled, err := Decode(payload)
			if err != nil || !enabled || instruction != test.want || original != "Не запускай команду 12 сентября." {
				t.Fatalf("Decode() = (%q, %q, %v, %v)", instruction, original, enabled, err)
			}
		})
	}
}

func TestEnvelopeRoundTripAndLegacyPayload(t *testing.T) {
	payload, err := Encode("clean", "raw text")
	if err != nil {
		t.Fatal(err)
	}
	instruction, text, enabled, err := Decode(payload)
	if err != nil || !enabled || instruction != "clean" || text != "raw text" {
		t.Fatalf("Decode()=(%q,%q,%v,%v)", instruction, text, enabled, err)
	}
	_, text, enabled, err = Decode([]byte("legacy prompt"))
	if err != nil || enabled || text != "legacy prompt" {
		t.Fatalf("legacy Decode() text=%q enabled=%v err=%v", text, enabled, err)
	}
}

func TestBypassOnlyShortSlashCommands(t *testing.T) {
	for _, text := range []string{"/model", "/model fast", "/model fast low", "/x a b c"} {
		if !Bypass(text) {
			t.Errorf("Bypass(%q)=false", text)
		}
	}
	if Bypass("/x a b c d") || Bypass("ordinary text") {
		t.Fatal("long command or ordinary text bypassed")
	}
}

func TestValidateResultUsesProportionalBound(t *testing.T) {
	if err := ValidateResult("short", "clean"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateResult("x", strings.Repeat("x", 1100)); err == nil {
		t.Fatal("oversized result accepted")
	}
}

func TestValidateResultRejectsToolProtocolOutput(t *testing.T) {
	for _, output := range []string{`<tool_call name="shell">x</tool_call>`, `{"type":"function_call","name":"exec"}`} {
		if err := ValidateResult("raw", output); err == nil {
			t.Fatalf("tool output %q accepted", output)
		}
	}
}

func TestEnvelopeRejectsTrailingJSON(t *testing.T) {
	payload, err := Encode("clean", "raw text")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, []byte(` {"text":"other"}`)...)
	if _, _, _, err := Decode(payload); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestPreparedEnvelopePersistsExactDecision(t *testing.T) {
	payload, err := Encode("clean", "raw text")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := MarkPrepared(payload, "clean text", true)
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(prepared)
	if err != nil || !state.Enabled || !state.Prepared || !state.Failed || state.Original != "raw text" || state.Processed != "clean text" {
		t.Fatalf("DecodeState() = %#v, %v", state, err)
	}
	_, text, enabled, err := Decode(prepared)
	if err != nil || !enabled || text != "clean text" {
		t.Fatalf("Decode() = (%q, %v, %v)", text, enabled, err)
	}
}

func TestMaximumRawInstructionRoundTripsWithSatelliteModeMarker(t *testing.T) {
	instruction := strings.Repeat("x", 16*1024)
	payload, err := EncodeMode(ModePerSession, instruction, "raw text")
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(payload)
	if err != nil || state.Instruction != instruction || state.Mode != ModePerSession {
		t.Fatalf("admitted state = (%d bytes, %q, %v)", len(state.Instruction), state.Mode, err)
	}
	prepared, err := MarkPrepared(payload, "clean text", false)
	if err != nil {
		t.Fatal(err)
	}
	state, err = DecodeState(prepared)
	if err != nil || !state.Prepared || state.Instruction != instruction || state.Processed != "clean text" {
		t.Fatalf("prepared state = (%d bytes, %#v, %v)", len(state.Instruction), state, err)
	}
}
