package nativecli

import "strings"

func statusDraftRetryEligible(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"starting mcp servers", "esc to interrupt", "escape to interrupt", "tab to queue message"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	lines := strings.Split(text, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(line, "›") {
			continue
		}
		if strings.TrimSpace(strings.TrimPrefix(line, "›")) != "/status" {
			return false
		}
		// Validate the exact same bottom composer/footer constraints without
		// modifying the terminal or accepting arbitrary slash input.
		lines[index] = "›"
		return ParseScreen(strings.Join(lines, "\n")).Ready
	}
	return false
}
