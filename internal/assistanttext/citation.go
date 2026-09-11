// Package assistanttext removes strictly identified service envelopes from
// assistant text without interpreting ordinary model-authored content.
package assistanttext

import (
	"path/filepath"
	"strconv"
	"strings"
)

const (
	openEnvelope     = "<oai-mem-citation>"
	closeEnvelope    = "</oai-mem-citation>"
	openCitations    = "<citation_entries>"
	closeCitations   = "</citation_entries>"
	openRollouts     = "<rollout_ids>"
	closeRollouts    = "</rollout_ids>"
	citationNoteMark = "|note=["
)

// StripTerminalMemoryCitation removes only a complete, valid memory-citation
// envelope occupying the end of an assistant final. Malformed, embedded and
// inline text is returned byte-for-byte unchanged.
func StripTerminalMemoryCitation(text string) string {
	terminal := strings.TrimRight(text, " \t\r\n")
	start := strings.LastIndex(terminal, openEnvelope)
	if start < 0 || start > 0 && terminal[start-1] != '\n' {
		return text
	}
	candidate := terminal[start:]
	if strings.Contains(candidate, "\r") {
		candidate = strings.ReplaceAll(candidate, "\r\n", "\n")
		if strings.Contains(candidate, "\r") {
			return text
		}
	}
	if !validEnvelope(strings.Split(candidate, "\n")) {
		return text
	}
	return strings.TrimRight(text[:start], " \t\r\n")
}

func validEnvelope(lines []string) bool {
	if len(lines) < 7 || lines[0] != openEnvelope || lines[1] != openCitations || lines[len(lines)-1] != closeEnvelope {
		return false
	}
	citationsEnd := indexLine(lines, closeCitations, 2)
	if citationsEnd < 0 || citationsEnd+1 >= len(lines) || lines[citationsEnd+1] != openRollouts {
		return false
	}
	rolloutsEnd := indexLine(lines, closeRollouts, citationsEnd+2)
	if rolloutsEnd != len(lines)-2 {
		return false
	}
	for _, entry := range lines[2:citationsEnd] {
		if !validCitationEntry(entry) {
			return false
		}
	}
	for _, id := range lines[citationsEnd+2 : rolloutsEnd] {
		if !validUUID(id) {
			return false
		}
	}
	return true
}

func indexLine(lines []string, target string, from int) int {
	for i := from; i < len(lines); i++ {
		if lines[i] == target {
			return i
		}
	}
	return -1
}

func validCitationEntry(entry string) bool {
	marker := strings.LastIndex(entry, citationNoteMark)
	if marker <= 0 || !strings.HasSuffix(entry, "]") {
		return false
	}
	location := entry[:marker]
	colon := strings.LastIndexByte(location, ':')
	dash := strings.LastIndexByte(location, '-')
	if colon <= 0 || dash <= colon+1 || dash == len(location)-1 {
		return false
	}
	path := location[:colon]
	start, startErr := strconv.Atoi(location[colon+1 : dash])
	end, endErr := strconv.Atoi(location[dash+1:])
	return startErr == nil && endErr == nil && start > 0 && end >= start &&
		path != "" && !filepath.IsAbs(path) && filepath.Clean(path) == path && path != "." && !strings.HasPrefix(path, "..")
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, character := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}
