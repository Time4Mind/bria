package runtimeprotocol

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateModelSelection bounds opaque provider values without inventing a catalog.
func ValidateModelSelection(model, effort string) error {
	for _, field := range []struct {
		value string
		max   int
	}{{model, 256}, {effort, 32}} {
		if len(field.value) > field.max || !utf8.ValidString(field.value) || strings.TrimSpace(field.value) != field.value {
			return ErrProtocol
		}
		for _, character := range field.value {
			if unicode.IsControl(character) || unicode.IsSpace(character) {
				return ErrProtocol
			}
		}
	}
	return nil
}
