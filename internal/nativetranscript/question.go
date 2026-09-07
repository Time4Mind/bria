package nativetranscript

import (
	"encoding/json"
	"strings"
)

// Only the native typed question tool is a question. Permission requests and
// arbitrary tool arguments are never inferred to be user-facing questions.
func userQuestion(b block) string {
	if b.Type != "tool_use" || b.Name != "AskUserQuestion" {
		return ""
	}
	var input struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if json.Unmarshal(b.Input, &input) != nil || len(input.Questions) == 0 || len(input.Questions) > 4 {
		return ""
	}
	parts := make([]string, 0, len(input.Questions))
	for _, question := range input.Questions {
		text := strings.TrimSpace(question.Question)
		if text == "" || len(text) > 4096 {
			return ""
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n")
}
