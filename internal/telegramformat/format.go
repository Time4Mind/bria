package telegramformat

import (
	"strings"
	"unicode/utf8"

	"bria/internal/telegram"
)

// Markdown converts the bounded Markdown emitted by providers into Telegram
// text entities. Entity offsets use Telegram's required UTF-16 units.
func Markdown(input string) (string, []telegram.MessageEntity) {
	var output strings.Builder
	entities := make([]telegram.MessageEntity, 0, 8)
	for len(input) > 0 {
		if strings.HasPrefix(input, "```") {
			if consumed, ok := appendFence(&output, &entities, input); ok {
				input = input[consumed:]
				continue
			}
			output.WriteString("```")
			input = input[3:]
			continue
		}
		if strings.HasPrefix(input, "**") {
			if consumed, ok := appendDelimited(&output, &entities, input, "**", "bold", false); ok {
				input = input[consumed:]
				continue
			}
		}
		if input[0] == '`' {
			if consumed, ok := appendDelimited(&output, &entities, input, "`", "code", true); ok {
				input = input[consumed:]
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(input)
		output.WriteRune(r)
		input = input[size:]
	}
	return output.String(), entities
}

func appendFence(output *strings.Builder, entities *[]telegram.MessageEntity, input string) (int, bool) {
	headerEnd := strings.IndexByte(input[3:], '\n')
	if headerEnd < 0 {
		return 0, false
	}
	headerEnd += 3
	language := input[3:headerEnd]
	if !validFenceLanguage(language) {
		return 0, false
	}
	contentStart := headerEnd + 1
	closingOffset := strings.Index(input[contentStart:], "```")
	if closingOffset < 0 {
		return 0, false
	}
	contentEnd := contentStart + closingOffset
	content := input[contentStart:contentEnd]
	if content == "" {
		return 0, false
	}
	offset := utf16Length(output.String())
	output.WriteString(content)
	*entities = append(*entities, telegram.MessageEntity{Type: "pre", Offset: offset, Length: utf16Length(content), Language: language})
	return contentEnd + 3, true
}

func appendDelimited(output *strings.Builder, entities *[]telegram.MessageEntity, input, delimiter, kind string, singleLine bool) (int, bool) {
	closingOffset := strings.Index(input[len(delimiter):], delimiter)
	if closingOffset < 0 {
		return 0, false
	}
	contentStart := len(delimiter)
	contentEnd := contentStart + closingOffset
	content := input[contentStart:contentEnd]
	if content == "" || singleLine && strings.ContainsRune(content, '\n') {
		return 0, false
	}
	offset := utf16Length(output.String())
	output.WriteString(content)
	*entities = append(*entities, telegram.MessageEntity{Type: kind, Offset: offset, Length: utf16Length(content)})
	return contentEnd + len(delimiter), true
}

func validFenceLanguage(language string) bool {
	if len(language) > 64 {
		return false
	}
	for _, character := range language {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("_+-", character) {
			continue
		}
		return false
	}
	return true
}

func utf16Length(value string) int {
	length := 0
	for _, character := range value {
		length++
		if character > 0xffff {
			length++
		}
	}
	return length
}
