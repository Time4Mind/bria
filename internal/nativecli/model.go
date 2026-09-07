package nativecli

import (
	"regexp"
	"strings"
)

var modelStatus = regexp.MustCompile(`(?i)^(?:current\s+)?model\s*:\s*(.+)$`)
var modelChanged = regexp.MustCompile(`(?i)^(?:set model to|kept model as|currently using)\s+(.+)$`)
var pickerNumber = regexp.MustCompile(`^[0-9]+[.)]\s*`)
var familyModel = regexp.MustCompile(`(?i)^(?:opus|sonnet|haiku|fable)(?:\s+[0-9]+(?:\.[0-9]+)?)?\b`)

// Native picker cursors indicate focus, not a committed choice. Only an
// explicit (current) marker, native status, or CLI change receipt sets Model.
func screenModel(text string) string {
	if len(text) > 1<<20 {
		return ""
	}
	lower := strings.ToLower(text)
	picker := strings.Contains(lower, "select model") || strings.Contains(lower, "select a model") || strings.Contains(lower, "choose a model")
	lines := strings.Split(text, "\n")
	if picker {
		for _, line := range lines {
			if i := strings.Index(strings.ToLower(line), "(current)"); i >= 0 {
				label := strings.TrimLeft(strings.TrimSpace(line[:i]), "│|›❯>✓✔ ")
				label = pickerNumber.ReplaceAllString(label, "")
				if model := modelLabel(label); model != "" {
					return model
				}
			}
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.Trim(strings.TrimSpace(lines[i]), "│| ")
		for _, pattern := range []*regexp.Regexp{modelStatus, modelChanged} {
			if m := pattern.FindStringSubmatch(strings.TrimLeft(line, "⎿ ")); len(m) > 1 {
				if model := modelLabel(m[1]); model != "" {
					return model
				}
			}
		}
		// Provider-generated compact footer/banner; not a bare model mention
		// inside an assistant response. Legacy uses model + effort + dot + cwd.
		if !picker && strings.Contains(line, " · ") {
			if strings.HasPrefix(line, "gpt-") || strings.HasPrefix(line, "codex-") ||
				(strings.Contains(lower, "claude code") && familyModel.MatchString(line)) {
				return modelLabel(line)
			}
		}
	}
	return ""
}

func modelLabel(label string) string {
	label = strings.TrimSpace(label)
	if m := familyModel.FindString(label); m != "" {
		return m
	}
	fields := strings.Fields(label)
	if len(fields) == 0 {
		return ""
	}
	value := strings.Trim(fields[0], "`│|()")
	if len(value) > 128 || strings.ContainsAny(value, "<>\x00") {
		return ""
	}
	return value
}
