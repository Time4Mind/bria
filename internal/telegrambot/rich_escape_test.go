package telegrambot

import "testing"

func TestRichTextEscapesUnknownAgentTagsWithoutBreakingDetails(t *testing.T) {
	rich, err := buildRichTextMessage(
		"<details><summary>✓ Read</summary>\n\n" +
			"<current_date>2026-08-15</current_date> x < y\n" +
			"`<literal>`\n```xml\n<inside-code>\n```\n\n</details>",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "<details><summary>✓ Read</summary>\n\n" +
		"&lt;current_date>2026-08-15&lt;/current_date> x &lt; y\n" +
		"`<literal>`\n```xml\n<inside-code>\n```\n\n</details>"
	if rich.Markdown != want {
		t.Fatalf("rich unknown-tag escaping=\n%s\nwant=\n%s", rich.Markdown, want)
	}
}
