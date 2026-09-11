package telegramsessions

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"bria/internal/domain"
)

type Lister interface {
	List(context.Context) ([]domain.Session, error)
}

func AvailableStoreName(ctx context.Context, sessions Lister, computerID domain.ComputerID, workdir string) (string, error) {
	values, err := sessions.List(ctx)
	if err != nil {
		return "", err
	}
	return AvailableName(values, computerID, workdir)
}

func RowSizes(count int) []int {
	result := make([]int, 0, (count+2)/3)
	for count > 0 {
		size := min(count, 3)
		result = append(result, size)
		count -= size
	}
	return result
}

func Labels(sessions []domain.Session, computerID domain.ComputerID) map[domain.SessionID]string {
	result, used := make(map[domain.SessionID]string), make(map[string]struct{})
	for _, session := range sessions {
		if session.ComputerID() == computerID && session.Name() != "" {
			result[session.ID()] = session.Name()
			used[strings.ToLower(session.Name())] = struct{}{}
		}
	}
	for _, session := range sessions {
		if session.ComputerID() != computerID || result[session.ID()] != "" {
			continue
		}
		result[session.ID()] = uniqueName(baseName(session.Workdir()), used)
		used[strings.ToLower(result[session.ID()])] = struct{}{}
	}
	return result
}

func AvailableName(sessions []domain.Session, computerID domain.ComputerID, workdir string) (string, error) {
	used := make(map[string]struct{})
	for _, label := range Labels(sessions, computerID) {
		used[strings.ToLower(label)] = struct{}{}
	}
	name := uniqueName(baseName(workdir), used)
	if name == "" {
		return "", errors.New("no unique session display name is available")
	}
	return name, nil
}

func LabelForID(ids []domain.SessionID, labels []string, target domain.SessionID) string {
	for index, id := range ids {
		if id == target && index < len(labels) {
			return strings.TrimPrefix(labels[index], "✓ ")
		}
	}
	return ""
}

func ShortID(id domain.SessionID) string {
	value := string(id)
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func uniqueName(base string, used map[string]struct{}) string {
	for ordinal := 1; ordinal < 10000; ordinal++ {
		suffix := ""
		if ordinal > 1 {
			suffix = strconv.Itoa(ordinal)
		}
		candidate := truncate(base, domain.MaxSessionNameRunes-len([]rune(suffix))) + suffix
		if _, exists := used[strings.ToLower(candidate)]; !exists {
			return candidate
		}
	}
	return ""
}

func baseName(workdir string) string {
	base := strings.TrimSpace(filepath.Base(filepath.Clean(workdir)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "Сессия"
	}
	base = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, base)
	words := strings.Fields(base)
	if len(words) == 0 {
		words = []string{"Сессия"}
	} else if len(words) > 2 {
		words = words[:2]
	}
	return truncate(strings.Join(words, " "), domain.MaxSessionNameRunes)
}

func truncate(value string, limit int) string {
	if limit < 1 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
