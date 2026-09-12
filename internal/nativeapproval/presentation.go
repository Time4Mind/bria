package nativeapproval

import (
	"regexp"
	"strings"
)

var interactiveHeading = regexp.MustCompile(`(?i)^(?:select\s|choose\s|settings:|do you want to\s|would you like to\s|hooks need review|restore the code|this command requires approval|bash command$|[☐✔☒])`)
var interactiveOption = regexp.MustCompile(`^[>›❯▶↑↓]?\s*\d+\.\s`)

// Extract the last actual picker, not historical status/account panels above
// it. Mobile layout follows legacy: option blocks, no terminal frame, bounded
// tail. Full raw-screen identity remains the adapter's responsibility.
func InteractiveContent(text string) (string, bool) {
	text = terminalCSI.ReplaceAllString(strings.ReplaceAll(text, "\r", ""), "")
	lines := strings.Split(text, "\n")
	top, bottom, option := -1, -1, -1
	for i := range lines {
		lines[i] = strings.TrimSpace(strings.Trim(strings.TrimSpace(lines[i]), "│┃║"))
		line := lines[i]
		if interactiveHeading.MatchString(line) && !strings.HasPrefix(line, "✔ You approved codex to ") {
			top, bottom = i, -1
		}
		if option < 0 && interactiveOption.MatchString(line) {
			option = i
		}
		lower := strings.ToLower(line)
		footer := strings.Contains(lower, "enter to select") || strings.Contains(lower, "esc to cancel") || strings.Contains(lower, "escape to cancel") || strings.Contains(lower, "press enter to confirm") || strings.Contains(lower, "esc to go back")
		if footer && top < 0 && option >= 0 {
			top = option
			// Include the adjacent question/title, never a preceding status
			// panel separated by a blank line or a key/value metadata row.
			if top > 0 && lines[top-1] != "" && !strings.Contains(lines[top-1], ":") {
				top--
			}
		}
		if footer && top >= 0 {
			bottom = i
		}
	}
	if top < 0 {
		return "", false
	}
	if bottom < top {
		bottom = len(lines) - 1
	}
	// A newly empty composer below a previously rendered selector proves that
	// the selector is now transcript history, not an input target.
	for _, line := range lines[top+1:] {
		if line == "›" || line == "❯" || line == ">" {
			return "", false
		}
	}
	var blocks []string
	var current []string
	flush := func() {
		if len(current) > 0 {
			blocks = append(blocks, strings.Join(current, "\n"))
			current = nil
		}
	}
	for _, line := range lines[top : bottom+1] {
		if strings.Trim(line, "─━═┌┐└┘╭╮╰╯+- ") == "" {
			continue
		}
		if interactiveOption.MatchString(line) {
			flush()
		} else if len(current) > 0 && (strings.Contains(strings.ToLower(line), "enter to") || strings.Contains(strings.ToLower(line), "esc to")) {
			flush()
		}
		current = append(current, line)
	}
	flush()
	content := strings.TrimSpace(strings.Join(blocks, "\n\n─────\n\n"))
	runes := []rune(content)
	if len(runes) > 3600 {
		content = string(runes[len(runes)-3600:])
	}
	return content, content != ""
}
