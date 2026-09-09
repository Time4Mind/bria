package sessioncreation

import "strings"

// DirectoryChoices preserves node ordering and never mutates a node-owned
// listing. Apply pagination limits afterward; roots pass hideDot=false.
func DirectoryChoices(items []Directory, hideDot bool) []Directory {
	if !hideDot {
		return items
	}
	choices := make([]Directory, 0, len(items))
	for _, item := range items {
		if !strings.HasPrefix(item.Name, ".") {
			choices = append(choices, item)
		}
	}
	return choices
}
