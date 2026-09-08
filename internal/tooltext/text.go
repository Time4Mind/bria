// Package tooltext retains bounded, readable tool content without I/O.
package tooltext

import (
	"encoding/json"
	"strings"
)

const Notice = "… (truncated)"
const Separator = "\n\n---\n\n"

type retained struct {
	Version   int    `json:"bria_tool_text"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Retain runs before provider metadata byte limits. Its JSON envelope is under
// 25 KiB even for escaped controls, within the existing 32 KiB protocol limit.
func Retain(raw string) string {
	text, truncated := Read(raw)
	text, cut := Bound(text, 40)
	encoded, _ := json.Marshal(retained{1, text, truncated || cut})
	return string(encoded)
}

// Read decodes native content arrays exactly once, preserving literal escapes.
func Read(raw string) (string, bool) {
	var value retained
	if json.Unmarshal([]byte(raw), &value) == nil && value.Version == 1 {
		return value.Text, value.Truncated
	}
	var blocks []struct{ Type, Text string }
	if strings.HasPrefix(strings.TrimSpace(raw), "[") && json.Unmarshal([]byte(raw), &blocks) == nil {
		var lines []string
		for _, block := range blocks {
			if block.Type == "text" || block.Type == "input_text" {
				lines = append(lines, block.Text)
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, "\n"), false
		}
	}
	return raw, false
}

// Bound counts Unicode scalars, with explicit and inserted newlines sharing
// one line budget. It never appends a truncation notice to content.
func Bound(text string, budget int) (string, bool) {
	if budget != 3 && budget != 5 && budget != 10 && budget != 20 && budget != 40 {
		budget = 10
	}
	var result strings.Builder
	line, column := 1, 0
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for _, r := range text {
		if r == '\n' || column == 100 {
			if line == budget {
				return result.String(), true
			}
			result.WriteByte('\n')
			line, column = line+1, 0
		}
		if r != '\n' {
			result.WriteRune(r)
			column++
		}
	}
	return result.String(), false
}
