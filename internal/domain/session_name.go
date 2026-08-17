package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SessionNameFormatVersion = 2
	MaxSessionNameRunes      = 12
)

// NormalizeSessionName applies the user-visible session-name contract: one or
// two words and no more than twelve Unicode characters in total.
func NormalizeSessionName(value string) (string, error) {
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("%w: session name contains control characters", ErrInvalidState)
		}
	}
	words := strings.Fields(value)
	if len(words) == 0 || len(words) > 2 {
		return "", fmt.Errorf("%w: session name must contain one or two words", ErrInvalidState)
	}
	name := strings.Join(words, " ")
	if utf8.RuneCountInString(name) > MaxSessionNameRunes {
		return "", fmt.Errorf("%w: session name exceeds %d characters", ErrInvalidState, MaxSessionNameRunes)
	}
	return name, nil
}

func (s *State) AvailableSessionName(ownerID UserID, ref SessionRef, requested string) (string, error) {
	name, err := NormalizeSessionName(requested)
	if err != nil {
		return "", err
	}
	if !s.sessionNameTaken(ownerID, ref, name) {
		return name, nil
	}
	for ordinal := 2; ordinal < 10000; ordinal++ {
		candidate := sessionNameWithSuffix(name, ordinal)
		if !s.sessionNameTaken(ownerID, ref, candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: no unique session name is available", ErrInvalidState)
}

func (s *State) sessionNameTaken(ownerID UserID, excluded SessionRef, name string) bool {
	for _, session := range s.Sessions {
		if session.OwnerID == ownerID && session.Ref() != excluded &&
			strings.EqualFold(strings.TrimSpace(session.Name), name) {
			return true
		}
	}
	return false
}

func sessionNameWithSuffix(name string, ordinal int) string {
	suffix := fmt.Sprintf("-%d", ordinal)
	limit := MaxSessionNameRunes - utf8.RuneCountInString(suffix)
	runes := []rune(name)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	base := strings.TrimSpace(string(runes))
	return base + suffix
}
