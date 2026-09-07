package runtimeprotocol

import "strings"

func validateNativeControl(message ParentMessage, limits Limits) error {
	if message.Command != "" && message.Key != "" || message.ExpectedHash != "" && !validNativeHash(message.ExpectedHash) {
		return ErrProtocol
	}
	if message.Command != "" {
		if !validRequiredText(message.Command, limits.MaxTextBytes) {
			return ErrProtocol
		}
		for _, r := range message.Command {
			if r < 32 && r != '\n' && r != '\t' || r == 127 {
				return ErrProtocol
			}
		}
	}
	switch message.Key {
	case "", "up", "down", "left", "right", "enter", "escape", "tab", "space":
		return nil
	default:
		return ErrProtocol
	}
}

func validNativeHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
