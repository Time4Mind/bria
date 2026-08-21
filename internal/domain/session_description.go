package domain

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ArchiveDescriptionVersion  = 1
	MaxArchiveDescriptionRunes = 180
)

// NormalizeArchiveDescription bounds the only model-produced content that is
// replicated for archive navigation. Raw prompts and transcript rows remain
// node-local.
func NormalizeArchiveDescription(lines []string) ([]string, error) {
	if len(lines) != 2 {
		return nil, errors.New("archive description must contain exactly two lines")
	}
	normalized := make([]string, len(lines))
	for index, line := range lines {
		if !utf8.ValidString(line) {
			return nil, errors.New("archive description must be valid UTF-8")
		}
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || utf8.RuneCountInString(line) > MaxArchiveDescriptionRunes {
			return nil, errors.New("archive description line is empty or too long")
		}
		for _, character := range line {
			if unicode.IsControl(character) {
				return nil, errors.New("archive description contains control characters")
			}
		}
		normalized[index] = line
	}
	return normalized, nil
}

func (s *State) SetArchiveDescription(
	ref SessionRef,
	expectedRevision uint64,
	archiveID string,
	lines []string,
	version int,
) error {
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	if session.State != SessionArchived || session.ArchiveID != archiveID ||
		version != ArchiveDescriptionVersion ||
		session.DescriptionVersion >= ArchiveDescriptionVersion {
		return ErrInvalidState
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}
	normalized, err := NormalizeArchiveDescription(lines)
	if err != nil {
		return ErrInvalidState
	}
	if session.Revision == math.MaxUint64 {
		return ErrInvalidState
	}
	session.ArchiveDescription = normalized
	session.DescriptionVersion = version
	session.Revision++
	s.Sessions[ref.Key()] = session
	return nil
}
