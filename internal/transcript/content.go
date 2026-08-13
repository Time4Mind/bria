package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
)

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// extractTextContent intentionally ignores image/base64 and all unknown
// structured blocks. It never decodes binary transcript content.
func extractTextContent(raw json.RawMessage, maxBodyBytes int) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return bounded(strings.TrimSpace(text), maxBodyBytes)
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		var item textContent
		if json.Unmarshal(raw, &item) == nil && allowedTextContent(item.Type) {
			return bounded(strings.TrimSpace(item.Text), maxBodyBytes)
		}
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		var direct string
		if json.Unmarshal(block, &direct) == nil {
			if direct = strings.TrimSpace(direct); direct != "" {
				parts = append(parts, direct)
			}
			continue
		}
		var item textContent
		if json.Unmarshal(block, &item) != nil {
			continue
		}
		if allowedTextContent(item.Type) {
			if text := strings.TrimSpace(item.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return bounded(strings.Join(parts, "\n"), maxBodyBytes)
}

func allowedTextContent(kind string) bool {
	switch kind {
	case "text", "input_text", "output_text", "text_result", "summary_text":
		return true
	default:
		return false
	}
}
