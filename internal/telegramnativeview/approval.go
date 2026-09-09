// Package telegramnativeview renders bounded native CLI interactions for
// Telegram without coupling terminal parsing to the Telegram transport.
package telegramnativeview

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"bria/internal/nativeapproval"
)

var terminalCSI = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[PX^_][^\x1b]*\x1b\\)`)

// CommandApproval turns only an exact Codex command-approval picker into Rich
// Markdown. Provider-owned values stay literal so punctuation is not markup.
func CommandApproval(text string) (string, bool) {
	request, ok := nativeapproval.ParseCodexCommandApproval(text)
	if !ok {
		return "", false
	}
	command := request.Preview
	if command == "" && request.Incomplete {
		command = collapsedApprovalPreview(text)
	}
	if command == "" {
		return "", false
	}
	sections := []string{"**Would you like to run the following command?**"}
	if request.Environment != "" {
		sections = append(sections, "**Environment**\n\n"+literalBlock("text", request.Environment, 100, 140))
	}
	if request.Reason != "" {
		sections = append(sections, "**Reason**\n\n"+literalBlock("text", request.Reason, 500, 560))
	}
	options := "› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)"
	if request.OptionCount == 3 {
		options = "› 1. Yes, proceed (y)\n  2. Yes, and don't ask again for this command prefix (p)\n  3. No, and tell Codex what to do differently (esc)"
	}
	sections = append(sections,
		"**Command**\n\n"+literalBlock("shell", command, 2200, 2100),
		"**Options**\n\n"+literalBlock("text", options, 240, 300),
		"_Press enter to confirm or esc to cancel_",
	)
	result := strings.Join(sections, "\n\n")
	if utf8.RuneCountInString(result) > 3600 {
		result = strings.Join([]string{
			sections[0], "**Command**\n\n" + literalBlock("shell", command, 1800, 1900),
			"**Options**\n\n" + literalBlock("text", options, 240, 300), sections[len(sections)-1],
		}, "\n\n")
	}
	return result, true
}

func collapsedApprovalPreview(text string) string {
	for _, line := range strings.Split(terminalCSI.ReplaceAllString(strings.ReplaceAll(text, "\r", ""), ""), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[…") && strings.Contains(line, "view all") {
			return line
		}
	}
	return ""
}

func literalBlock(language, value string, maxContentRunes, maxBlockRunes int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\r", ""))
	runes := utf8.RuneCountInString(value)
	if runes > maxContentRunes {
		runes = maxContentRunes
	}
	best := renderLiteralBlock(language, "")
	for low, high := 0, runes; low <= high; {
		middle := low + (high-low)/2
		candidate := renderLiteralBlock(language, boundRunes(value, middle))
		if utf8.RuneCountInString(candidate) <= maxBlockRunes {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best
}

func renderLiteralBlock(language, value string) string {
	fence := "```"
	for strings.Contains(value, fence) {
		fence += "`"
	}
	return fence + language + "\n" + value + "\n" + fence
}

func boundRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	const marker = "\n[… truncated]"
	markerRunes := []rune(marker)
	if limit <= len(markerRunes) {
		return string(markerRunes[:limit])
	}
	runes := []rune(value)
	return string(runes[:limit-len(markerRunes)]) + marker
}
