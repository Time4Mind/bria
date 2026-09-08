package markdownliteral_test

import (
	"reflect"
	"testing"

	"bria/internal/markdownliteral"
)

func TestLinesRequireWholeClosingFence(t *testing.T) {
	lines := []string{"~~~md", "literal", "~~~ ~~~", "still literal", "~~~", "ordinary"}
	want := []bool{true, true, true, true, true, false}
	if got := markdownliteral.Lines(lines); !reflect.DeepEqual(got, want) {
		t.Fatalf("literal lines = %v, want %v", got, want)
	}
}

func TestLinesPreservesIndependentCodeBoundaries(t *testing.T) {
	for name, tc := range map[string]struct {
		lines []string
		want  []bool
	}{
		"ordinary inline code": {[]string{"| `A` | B |", "|---|---|"}, []bool{false, false}},
		"nested HTML":          {[]string{"<PRE><code class=\"language-md\">", "body", "</code>", "still pre", "</PRE>", "ordinary"}, []bool{false, true, true, true, true, false}},
		"multiline backticks":  {[]string{"``", "`", "<code>", "``", "ordinary"}, []bool{false, true, true, true, false}},
		"long fence":           {[]string{"````md", "```", "~~~", "<code>", "`````", "ordinary"}, []bool{true, true, true, true, true, false}},
		"escaped backtick":     {[]string{"escaped \\`", "ordinary"}, []bool{false, false}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := markdownliteral.Lines(tc.lines); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("literal lines = %v, want %v", got, tc.want)
			}
		})
	}
}
