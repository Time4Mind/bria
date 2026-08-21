package processlog

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxIdentityBytes       = 128
	maxStructuredBodyBytes = 8 << 10
)

func (identity Identity) validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{{"version", identity.Version}, {"commit", identity.Commit}} {
		value := strings.TrimSpace(field.value)
		if value == "" || len(value) > maxIdentityBytes || !utf8.ValidString(value) {
			return fmt.Errorf("process log %s is invalid", field.name)
		}
		for _, char := range value {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
				char >= '0' && char <= '9' || strings.ContainsRune("._+-", char)) {
				return fmt.Errorf("process log %s is invalid", field.name)
			}
		}
	}
	return nil
}

func formatStructuredRecord(
	at time.Time,
	pid int,
	identity Identity,
	level Level,
	class FailureClass,
	message string,
) []byte {
	body := sanitizeStructuredBody(message)
	truncated := false
	if len(body) > maxStructuredBodyBytes {
		body = body[:maxStructuredBodyBytes]
		for !utf8.ValidString(body) {
			body = body[:len(body)-1]
		}
		body += "..."
		truncated = true
	}
	return fmt.Appendf(
		nil,
		"at=%s pid=%d version=%q commit=%q severity=%s failure_class=%s truncated=%t %s\n",
		at.UTC().Format(time.RFC3339Nano), pid, identity.Version, identity.Commit,
		normalizedLevel(level), normalizeFailureClass(class), truncated, body,
	)
}

func sanitizeStructuredBody(message string) string {
	message = strings.ToValidUTF8(message, "?")
	message = redactAbsolutePaths(message)
	var result bytes.Buffer
	result.Grow(len(message))
	for index := 0; index < len(message); {
		char, size := utf8.DecodeRuneInString(message[index:])
		switch char {
		case '\r':
			result.WriteString(`\r`)
		case '\n':
			result.WriteString(`\n`)
		case '\t':
			result.WriteString(`\t`)
		default:
			if char < 0x20 || char == 0x7f {
				fmt.Fprintf(&result, `\x%02x`, char)
			} else {
				result.WriteRune(char)
			}
		}
		index += size
	}
	return result.String()
}

func redactAbsolutePaths(message string) string {
	var result strings.Builder
	result.Grow(len(message))
	for index := 0; index < len(message); {
		if message[index] != '/' || (index > 0 && !pathBoundary(message[index-1])) {
			result.WriteByte(message[index])
			index++
			continue
		}
		end := index + 1
		for end < len(message) && !pathTerminator(message[end]) {
			end++
		}
		result.WriteString("[path]")
		index = end
	}
	return result.String()
}

func pathBoundary(char byte) bool {
	switch char {
	case ' ', '\t', '\r', '\n', '=', '(', '[', '{', '"', '\'', ',':
		return true
	default:
		return false
	}
}

func pathTerminator(char byte) bool {
	switch char {
	case ' ', '\t', '\r', '\n', ')', ']', '}', '"', '\'', ',':
		return true
	default:
		return false
	}
}
