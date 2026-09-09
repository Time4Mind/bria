package promptpreprocess

import (
	"strings"
	"testing"
)

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
