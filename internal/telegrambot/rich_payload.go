package telegrambot

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func buildRichTextMessage(text string) (richMessage, error) {
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxRichTextRunes {
		return richMessage{}, errors.New("invalid or oversized rich screen text")
	}
	return richMessage{Markdown: normalizeRichTables(text)}, nil
}

// normalizeRichTables mirrors the small table normalization used by CCBot.
// Telegram's Rich Markdown parser requires a blank line before a GFM table;
// <sub> keeps four-column status tables readable on narrow phone screens.
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

func richFallbackText(screen telegramui.Screen) string {
	text := screen.Text
	text = strings.ReplaceAll(text, "<details><summary>", "")
	text = strings.ReplaceAll(text, "</summary>\n\n", "\n")
	text = strings.ReplaceAll(text, "</details>", "")
	return html.UnescapeString(text)
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
	markdown := insertPaneAnchor(text, pane.AnchorOffset)
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
