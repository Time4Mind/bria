package updateinstall

import "strings"

func invalidText(value string, maximum int) bool {
	return value == "" || len(value) > maximum || value != strings.TrimSpace(value) || strings.ContainsRune(value, 0)
}
