package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
)

func codexToolCall(name string, raw json.RawMessage, maxBodyBytes int) (string, string) {
	decoded := decodeCodexArguments(raw)
	if source, ok := decoded.(string); ok {
		if nestedName, arguments, found := embeddedCodexToolCall(source); found {
			return displayCodexToolName(nestedName), formatCodexToolArguments(arguments, maxBodyBytes)
		}
		return name, bounded(strings.TrimSpace(source), maxBodyBytes)
	}
	return name, formatCodexToolArguments(decoded, maxBodyBytes)
}

func decodeCodexArguments(raw json.RawMessage) any {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return nil
	}
	if encoded, ok := decoded.(string); ok {
		encoded = strings.TrimSpace(encoded)
		var nested any
		if json.Unmarshal([]byte(encoded), &nested) == nil {
			return nested
		}
		return encoded
	}
	return decoded
}

func embeddedCodexToolCall(source string) (string, any, bool) {
	const marker = "tools."
	for offset := 0; offset < len(source); {
		index := strings.Index(source[offset:], marker)
		if index < 0 {
			return "", nil, false
		}
		start := offset + index + len(marker)
		end := start
		for end < len(source) {
			r := rune(source[end])
			if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				break
			}
			end++
		}
		cursor := end
		for cursor < len(source) && unicode.IsSpace(rune(source[cursor])) {
			cursor++
		}
		if end > start && cursor < len(source) && source[cursor] == '(' {
			argumentsSource := quoteJavaScriptObjectKeys(source[cursor+1:])
			decoder := json.NewDecoder(strings.NewReader(argumentsSource))
			var arguments any
			if decoder.Decode(&arguments) == nil {
				return source[start:end], arguments, true
			}
		}
		offset = start
	}
	return "", nil, false
}

func quoteJavaScriptObjectKeys(source string) string {
	var result strings.Builder
	result.Grow(len(source) + 16)
	for index := 0; index < len(source); {
		if source[index] == '"' {
			end := index + 1
			for end < len(source) {
				if source[end] == '\\' {
					end += 2
					continue
				}
				end++
				if source[end-1] == '"' {
					break
				}
			}
			result.WriteString(source[index:end])
			index = end
			continue
		}
		if isJavaScriptIdentifierStart(source[index]) {
			end := index + 1
			for end < len(source) && isJavaScriptIdentifierPart(source[end]) {
				end++
			}
			lookahead := end
			for lookahead < len(source) && unicode.IsSpace(rune(source[lookahead])) {
				lookahead++
			}
			if lookahead < len(source) && source[lookahead] == ':' {
				result.WriteByte('"')
				result.WriteString(source[index:end])
				result.WriteByte('"')
				index = end
				continue
			}
		}
		result.WriteByte(source[index])
		index++
	}
	return result.String()
}

func isJavaScriptIdentifierStart(value byte) bool {
	return value == '_' || value == '$' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isJavaScriptIdentifierPart(value byte) bool {
	return isJavaScriptIdentifierStart(value) || value >= '0' && value <= '9'
}

func displayCodexToolName(name string) string {
	switch name {
	case "exec_command":
		return "Bash"
	default:
		return name
	}
}

func formatCodexToolArguments(arguments any, maxBodyBytes int) string {
	if arguments == nil {
		return ""
	}
	if object, ok := arguments.(map[string]any); ok {
		if command, ok := stringArgument(object, "cmd", "command"); ok {
			return bounded("```bash\n"+strings.TrimSpace(command)+"\n```", maxBodyBytes)
		}
	}
	encoded, err := json.MarshalIndent(arguments, "", "  ")
	if err != nil {
		return ""
	}
	return bounded(string(encoded), maxBodyBytes)
}

func stringArgument(arguments map[string]any, names ...string) (string, bool) {
	for _, name := range names {
		value, ok := arguments[name].(string)
		if ok && strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	return "", false
}
