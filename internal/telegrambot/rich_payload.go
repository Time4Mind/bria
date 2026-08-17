package telegrambot

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func buildRichTextMessage(text string) (richMessage, error) {
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxRichTextRunes {
		return richMessage{}, errors.New("invalid or oversized rich screen text")
	}
	return richMessage{Markdown: normalizeRichMarkdown(text)}, nil
}

func normalizeRichMarkdown(text string) string {
	text = normalizeRichTables(normalizeRichShellFences(text))
	return escapeUnsupportedRichTags(text)
}

var allowedRichTags = map[string]struct{}{
	"a": {}, "aside": {}, "audio": {}, "b": {}, "blockquote": {}, "br": {},
	"caption": {}, "cite": {}, "code": {}, "del": {}, "details": {}, "em": {},
	"figcaption": {}, "figure": {}, "footer": {}, "h1": {}, "h2": {}, "h3": {},
	"h4": {}, "h5": {}, "h6": {}, "hr": {}, "i": {}, "img": {}, "ins": {},
	"li": {}, "mark": {}, "ol": {}, "p": {}, "pre": {}, "s": {}, "strike": {},
	"strong": {}, "sub": {}, "summary": {}, "sup": {}, "table": {}, "tbody": {},
	"td": {}, "tfoot": {}, "th": {}, "thead": {}, "tr": {}, "u": {}, "ul": {},
	"video":      {},
	"tg-collage": {}, "tg-emoji": {}, "tg-map": {}, "tg-math": {},
	"tg-math-block": {}, "tg-reference": {}, "tg-slideshow": {}, "tg-spoiler": {},
	"tg-time": {},
}

// Telegram's Rich Markdown parser silently consumes unknown tag-shaped text.
// Agent transcripts routinely contain XML-like context or tool output, so
// mirror CCBot and escape every unsupported '<' outside Markdown code. Native
// Rich tags, including the <details> blocks used for tool calls, stay intact.
func escapeUnsupportedRichTags(text string) string {
	var result strings.Builder
	result.Grow(len(text))
	for index := 0; index < len(text); {
		if strings.HasPrefix(text[index:], "```") {
			closing := strings.Index(text[index+3:], "```")
			if closing < 0 {
				result.WriteString(text[index:])
				break
			}
			closing += index + 6
			result.WriteString(text[index:closing])
			index = closing
			continue
		}
		if text[index] == '`' {
			closing := strings.IndexByte(text[index+1:], '`')
			newline := strings.IndexByte(text[index+1:], '\n')
			if closing >= 0 && (newline < 0 || closing < newline) {
				closing += index + 2
				result.WriteString(text[index:closing])
				index = closing
				continue
			}
		}
		if text[index] == '<' && !startsAllowedRichTag(text[index:]) {
			result.WriteString("&lt;")
			index++
			continue
		}
		result.WriteByte(text[index])
		index++
	}
	return result.String()
}

func startsAllowedRichTag(text string) bool {
	if len(text) < 3 || text[0] != '<' {
		return false
	}
	end := strings.IndexByte(text, '>')
	if end < 0 || end > 512 {
		return false
	}
	content := strings.TrimSpace(text[1:end])
	content = strings.TrimPrefix(content, "/")
	if content == "" {
		return false
	}
	nameEnd := strings.IndexAny(content, " \t\r\n/")
	if nameEnd >= 0 {
		content = content[:nameEnd]
	}
	_, ok := allowedRichTags[strings.ToLower(content)]
	return ok
}

var copyableRichShellLanguages = map[string]struct{}{
	"": {}, "bash": {}, "bat": {}, "batch": {}, "cmd": {}, "console": {},
	"powershell": {}, "ps1": {}, "pwsh": {}, "sh": {}, "shell": {}, "zsh": {},
}

// Telegram Android displays fenced shell blocks literally inside Rich
// <details>. CCBot works around that client behavior by using a multi-line
// Rich <code> span, which also restores the native tap/copy interaction.
func normalizeRichShellFences(text string) string {
	lines := strings.Split(text, "\n")
	normalized := make([]string, 0, len(lines))
	for index := 0; index < len(lines); {
		opener := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(opener, "```") || strings.Contains(opener[3:], "`") {
			normalized = append(normalized, lines[index])
			index++
			continue
		}
		language := strings.ToLower(strings.TrimSpace(opener[3:]))
		if _, ok := copyableRichShellLanguages[language]; !ok {
			normalized = append(normalized, lines[index])
			index++
			continue
		}
		closing := index + 1
		for closing < len(lines) && strings.TrimSpace(lines[closing]) != "```" {
			closing++
		}
		if closing == len(lines) || closing == index+1 {
			normalized = append(normalized, lines[index])
			index++
			continue
		}
		body := strings.Join(lines[index+1:closing], "\n")
		if !strings.Contains(body, "\n") && !strings.Contains(body, "`") {
			normalized = append(normalized, "`"+body+"`")
		} else {
			normalized = append(normalized, "<code>"+escapeRichCode(body)+"</code>")
		}
		index = closing + 1
	}
	return strings.Join(normalized, "\n")
}

func escapeRichCode(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	return strings.ReplaceAll(text, ">", "&gt;")
}

// normalizeRichTables mirrors the small table normalization used by CCBot.
// Telegram's Rich Markdown parser requires a blank line before a GFM table;
// <sub> keeps compact status tables readable on narrow phone screens.
func normalizeRichTables(text string) string {
	lines := strings.Split(text, "\n")
	withBlanks := make([]string, 0, len(lines)+1)
	inFence := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(trimmed, "|") && index+1 < len(lines) &&
			isRichTableSeparator(lines[index+1]) && len(withBlanks) > 0 &&
			strings.TrimSpace(withBlanks[len(withBlanks)-1]) != "" &&
			!strings.HasPrefix(strings.TrimSpace(withBlanks[len(withBlanks)-1]), "|") {
			withBlanks = append(withBlanks, "")
		}
		withBlanks = append(withBlanks, line)
	}
	for index := 0; index < len(withBlanks); {
		if !strings.HasPrefix(strings.TrimSpace(withBlanks[index]), "|") {
			index++
			continue
		}
		end := index
		for end < len(withBlanks) && strings.HasPrefix(strings.TrimSpace(withBlanks[end]), "|") {
			end++
		}
		if end-index >= 2 {
			for row := index; row < end; row++ {
				if !isRichTableSeparator(withBlanks[row]) {
					withBlanks[row] = subWrapRichTableRow(withBlanks[row])
				}
			}
		}
		index = end
	}
	return strings.Join(withBlanks, "\n")
}

func isRichTableSeparator(line string) bool {
	cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		value := strings.Trim(strings.TrimSpace(cell), ":")
		if len(value) < 3 || strings.Trim(value, "-") != "" {
			return false
		}
	}
	return true
}

func subWrapRichTableRow(line string) string {
	cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for index, cell := range cells {
		value := strings.TrimSpace(cell)
		if value != "" && !strings.HasPrefix(value, "<sub>") {
			cells[index] = " <sub>" + value + "</sub> "
		}
	}
	return "|" + strings.Join(cells, "|") + "|"
}

const (
	richPhotoID       = "terminal_screenshot"
	richPhotoFilename = "terminal_screenshot.png"
	richPhotoMarkdown = "![](tg://photo?id=" + richPhotoID + ")"
	richMediaSpacer   = "\u00a0"
)

func buildRichMessage(text string, pane telegramui.PaneImage, photoReference string) (richMessage, error) {
	if !utf8.ValidString(text) || pane.AnchorOffset < 0 || pane.AnchorOffset > len(text) ||
		!utf8.ValidString(text[:pane.AnchorOffset]) {
		return richMessage{}, errors.New("invalid rich screen text or media anchor")
	}
	markdown := normalizeRichMarkdown(insertPaneAnchor(text, pane.AnchorOffset))
	if utf8.RuneCountInString(markdown) > MaxRichTextRunes {
		return richMessage{}, errors.New("rich screen text exceeds Telegram limit")
	}
	if !validPhotoReference(photoReference) {
		return richMessage{}, errors.New("invalid rich pane photo reference")
	}
	return richMessage{
		Markdown: markdown,
		Media: []richMediaItem{{
			ID:    richPhotoID,
			Media: inputMediaPhoto{Type: "photo", Media: photoReference},
		}},
	}, nil
}

func insertPaneAnchor(text string, offset int) string {
	if offset <= 0 || offset >= len(text) {
		return strings.TrimRight(text, "\n") + "\n\n" + richMediaSpacer + "\n\n" +
			richPhotoMarkdown
	}
	head := strings.TrimRight(text[:offset], "\n")
	tail := strings.TrimLeft(text[offset:], "\n")
	if tail == "" {
		return head + "\n\n" + richMediaSpacer + "\n\n" + richPhotoMarkdown
	}
	// Session cards already place a non-breaking spacer before context and
	// background metadata. The media shell contributes its own spacer after the
	// screenshot, so coalesce the existing one instead of rendering two blank
	// bands between the image and the metadata.
	tail = strings.TrimPrefix(tail, richMediaSpacer+"\n\n")
	return head + "\n\n" + richMediaSpacer + "\n\n" + richPhotoMarkdown +
		"\n\n" + richMediaSpacer + "\n\n" + tail
}

func paneReference(pane telegramui.PaneImage, previous Message) (string, []byte, error) {
	if len(pane.PNG) > 0 && pane.Hash != "" && pane.Hash == previous.PaneHash &&
		previous.RichMediaFileID != "" {
		return previous.RichMediaFileID, nil, nil
	}
	if len(pane.PNG) > 0 {
		if len(pane.PNG) > MaxRichPanePNGBytes || !bytes.HasPrefix(pane.PNG, []byte("\x89PNG\r\n\x1a\n")) {
			return "", nil, errors.New("pane image is not a bounded PNG")
		}
		return "attach://" + richPhotoID, pane.PNG, nil
	}
	fileID := strings.TrimSpace(pane.FileID)
	if fileID == "" {
		fileID = previous.RichMediaFileID
	}
	if !validFileID(fileID) {
		return "", nil, errors.New("pane image has no valid Telegram file ID")
	}
	return fileID, nil, nil
}

func validPhotoReference(value string) bool {
	return value == "attach://"+richPhotoID || validFileID(value)
}

func validFileID(value string) bool {
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func marshalField(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
