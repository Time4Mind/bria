package assistanttext

import "testing"

const validCitation = `<oai-mem-citation>
<citation_entries>
MEMORY.md:12-14|note=[session context]
</citation_entries>
<rollout_ids>
019c6e27-e55b-73d1-87d8-4e01f1f75043
</rollout_ids>
</oai-mem-citation>`

func TestStripTerminalMemoryCitation(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "separated terminal envelope", in: "Готово.\n\n" + validCitation, want: "Готово."},
		{name: "terminal envelope with trailing whitespace", in: "Готово.\r\n\r\n" + validCitation + "\r\n ", want: "Готово."},
		{name: "only envelope", in: validCitation, want: ""},
		{name: "ordinary memory mention", in: "Проверен MEMORY.md:12-14", want: "Проверен MEMORY.md:12-14"},
		{name: "embedded envelope", in: validCitation + "\nvisible", want: validCitation + "\nvisible"},
		{name: "malformed missing section", in: "Готово.\n\n<oai-mem-citation>\n<citation_entries>\nMEMORY.md:12-14\n</citation_entries>\n</oai-mem-citation>", want: "Готово.\n\n<oai-mem-citation>\n<citation_entries>\nMEMORY.md:12-14\n</citation_entries>\n</oai-mem-citation>"},
		{name: "malformed inline open", in: "Готово. <oai-mem-citation>\n<citation_entries>\n</citation_entries>\n<rollout_ids>\n</rollout_ids>\n</oai-mem-citation>", want: "Готово. <oai-mem-citation>\n<citation_entries>\n</citation_entries>\n<rollout_ids>\n</rollout_ids>\n</oai-mem-citation>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := StripTerminalMemoryCitation(test.in); got != test.want {
				t.Fatalf("StripTerminalMemoryCitation() = %q, want %q", got, test.want)
			}
		})
	}
}
