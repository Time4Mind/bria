package tooltext_test

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/tooltext"
)

func TestDecodedToolCanBeStoredAgainWithoutReinterpretingLiteralJSON(t *testing.T) {
	literal := `[{"type":"text","text":"literal example"}]`
	raw, _ := json.Marshal([]map[string]string{{"type": "text", "text": literal}})
	encoded := tooltext.Encode(tooltext.Tool{Name: "exec", Output: tooltext.Retain(string(raw))})
	decoded, ok := tooltext.Decode(encoded)
	if !ok {
		t.Fatal("decode failed")
	}
	again, ok := tooltext.Decode(tooltext.Encode(decoded))
	if !ok || again.Output != literal {
		t.Fatalf("second storage reinterpreted content: %q", again.Output)
	}
}

func TestRetainDecodesBeforeBoundingAndReportsTruncation(t *testing.T) {
	for _, glyph := range []string{"🙂", "<", "\x01"} {
		raw, _ := json.Marshal([]map[string]string{{"type": "input_text", "text": strings.Repeat(glyph, 10000)}})
		retained := tooltext.Retain(string(raw))
		if len(retained) >= 25*1024 || !json.Valid([]byte(retained)) {
			t.Fatal("unbounded transport text")
		}
		text, cut := tooltext.Read(retained)
		want := strings.TrimSuffix(strings.Repeat(strings.Repeat(glyph, 100)+"\n", 40), "\n")
		if text != want || !cut {
			t.Fatal("lost readable prefix or notice")
		}
	}
}

func TestCompactHistoryWorstCaseMetadataRemainsBounded(t *testing.T) {
	var varied strings.Builder
	for i := 0; i < 4000; i++ {
		varied.WriteRune(rune(0x10000 + i*211%0xfffff))
	}
	for _, content := range []string{varied.String(), strings.Repeat("\x01", 4000), strings.Repeat("<", 4000)} {
		tool := tooltext.Tool{ID: strings.Repeat("\x01", 170), Name: strings.Repeat("\x01", 1000), Status: strings.Repeat("\x01", 1000), Output: content}
		encoded := tooltext.Encode(tool)
		if len(encoded) > 16384 || !utf8.ValidString(encoded) || !json.Valid([]byte(encoded)) {
			t.Fatalf("history bytes=%d", len(encoded))
		}
		decoded, ok := tooltext.Decode(encoded)
		if !ok || strings.ReplaceAll(decoded.Output, "\n", "") != content || decoded.Truncated {
			t.Fatal("compact content changed")
		}
	}
}

func TestCompactCorruptionFailsExplicitly(t *testing.T) {
	for _, value := range []string{"?", "AA", "A", strings.Repeat("A", 14141)} {
		raw, _ := json.Marshal(tooltext.Tool{Name: "exec", Encoding: "rune21-v1", Output: value})
		if _, ok := tooltext.Decode(string(raw)); ok {
			t.Fatalf("accepted corrupt compact value of size %d", len(value))
		}
	}
}

func TestBoundExplicitAndInsertedNewlinesShareBudget(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
		cut               bool
	}{
		{"exact width", strings.Repeat("я", 100), strings.Repeat("я", 100), false},
		{"wrap", strings.Repeat("я", 101), strings.Repeat("я", 100) + "\nя", false},
		{"exact width newline", strings.Repeat("я", 100) + "\nnext", strings.Repeat("я", 100) + "\nnext", false},
		{"blank lines", "\n\n\n\n", "\n\n\n\n", false},
		{"sixth empty line", "\n\n\n\n\n", "\n\n\n\n", true},
		{"mixed", strings.Repeat("я", 201) + "\r\n\r\nlast\r\nhidden", strings.Repeat("я", 100) + "\n" + strings.Repeat("я", 100) + "\nя\n\nlast", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, cut := tooltext.Bound(test.input, 5)
			if got != test.want || cut != test.cut {
				t.Fatalf("got %q cut=%t", got, cut)
			}
		})
	}
}

func TestCompactRoundTripBothFieldsAndResave(t *testing.T) {
	tool := tooltext.Tool{ID: "call", Name: "exec", Arguments: strings.Repeat("\x01", 1800), Output: strings.Repeat("🙂", 1800)}
	encoded := tooltext.Encode(tool)
	decoded, ok := tooltext.Decode(encoded)
	if !ok || decoded.Truncated || strings.ReplaceAll(decoded.Arguments, "\n", "") != tool.Arguments || strings.ReplaceAll(decoded.Output, "\n", "") != tool.Output {
		t.Fatal("combined fields changed")
	}
	again, ok := tooltext.Decode(tooltext.Encode(decoded))
	if !ok || again != decoded {
		t.Fatal("decode/encode not stable")
	}
}

func TestA25TrailingCarriageReturnKeepsFirstOutputRune(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
		compact            bool
	}{
		{"ascii", "XYZ", "XYZ", false},
		{"emoji", "🙂XYZ", "🙂XYZ", false},
		{"compact_forty_lines", strings.Repeat("\x01", 3600), strings.TrimSuffix(strings.Repeat(strings.Repeat("\x01", 100)+"\n", 36), "\n"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded := tooltext.Encode(tooltext.Tool{Name: "exec", Arguments: "a\r", Output: tc.output})
			if len(encoded) > 16384 || !json.Valid([]byte(encoded)) || !utf8.ValidString(encoded) {
				t.Fatal("invalid or oversized persisted envelope")
			}
			if tc.compact && !strings.Contains(encoded, `"encoding":"rune21-v1"`) {
				t.Fatal("fixture did not exercise compact storage")
			}
			decoded, ok := tooltext.Decode(encoded)
			if !ok || decoded.Arguments != "a\r" || decoded.Output != tc.want || decoded.Truncated || !utf8.ValidString(decoded.Output) {
				t.Fatalf("boundary changed output: decoded=%t arguments=%q output_runes=%d want_runes=%d truncated=%t", ok, decoded.Arguments, utf8.RuneCountInString(decoded.Output), utf8.RuneCountInString(tc.want), decoded.Truncated)
			}
			again, ok := tooltext.Decode(tooltext.Encode(decoded))
			if !ok || again != decoded {
				t.Fatal("resaving shifted the output boundary again")
			}
		})
	}
}
