package application

import (
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/domain"
)

const (
	DefaultCardPageRunes = 3500
	DefaultCardMaxPages  = 64
	// Telegram's legacy text carrier is limited by encoded payload size. Keep
	// enough room for the session header, background panel, and keyboard even
	// when a Rich Markdown card has to fall back to editMessageText.
	DefaultCardPageBytes  = 3000
	cardEventJoiner       = "\n\n\u00a0\n\n"
	defaultTechnicalLines = 15
	maxTechnicalSummary   = 64
)

type CardRenderOptions struct {
	Now               time.Time
	Location          *time.Location
	MaxPageRunes      int
	MaxPageBytes      int
	MaxPages          int
	MaxTechnicalLines int
}

type CardEventPage struct {
	RichMarkdown string
	Anchor       string
	AnchorIndex  string
	Number       int
	Count        int
}

type CardEventPages struct {
	Pages               []CardEventPage
	Latest              CardEventPage
	LatestResponseStart CardEventPage
}

// RenderCardEventPages renders a stable, bounded live-card history. Technical
// blocks use the same Rich Markdown details construct as CCBot; conversation
// text remains inline. The latest page is always returned explicitly.
func RenderCardEventPages(
	preferences domain.UserPreferences,
	events []CardEvent,
	options CardRenderOptions,
) CardEventPages {
	if options.MaxTechnicalLines <= 0 {
		options.MaxTechnicalLines = preferences.EffectiveToolOutputLines()
	}
	options = normalizedCardRenderOptions(options)
	prepared := prepareCardEvents(preferences, events)
	blocks := renderCardEventBlocks(prepared, options)
	packed := packCardEventPages(blocks, options.MaxPageRunes, options.MaxPageBytes)
	packed = retainLatestCardPages(packed, options.MaxPages)
	pages := make([]CardEventPage, len(packed.pages))
	for index, packedPage := range packed.pages {
		pages[index] = CardEventPage{
			RichMarkdown: packedPage.text,
			Anchor:       packedPage.anchor,
			AnchorIndex:  strings.Join(packedPage.anchors, "\x00"),
			Number:       index + 1,
			Count:        len(packed.pages),
		}
	}
	result := CardEventPages{Pages: pages}
	if len(pages) > 0 {
		result.Latest = pages[len(pages)-1]
	}
	if packed.latestResponseStart > 0 && packed.latestResponseStart <= len(pages) {
		result.LatestResponseStart = pages[packed.latestResponseStart-1]
	}
	return result
}

func normalizedCardRenderOptions(options CardRenderOptions) CardRenderOptions {
	if options.MaxPageRunes <= 0 {
		options.MaxPageRunes = DefaultCardPageRunes
	}
	if options.MaxPageBytes <= 0 {
		options.MaxPageBytes = DefaultCardPageBytes
	} else if options.MaxPageBytes < utf8.UTFMax {
		options.MaxPageBytes = utf8.UTFMax
	}
	if options.MaxPages <= 0 {
		options.MaxPages = DefaultCardMaxPages
	}
	if options.MaxTechnicalLines <= 0 {
		options.MaxTechnicalLines = defaultTechnicalLines
	}
	if options.Location == nil {
		options.Location = time.Local
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	return options
}

type cardEventBlock struct {
	text          string
	anchor        string
	pageBreak     bool
	responseStart bool
}

func renderCardEventBlocks(events []CardEvent, options CardRenderOptions) []cardEventBlock {
	blocks := make([]cardEventBlock, 0, len(events))
	for index, event := range events {
		inFlight := index == len(events)-1 && event.CompletedAt == nil &&
			(event.Kind == CardEventToolCall || event.Kind == CardEventThinking)
		text := renderCardEvent(event, inFlight, options)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if event.Kind == CardEventAssistantText {
			chunks := splitCardText(text, options.MaxPageRunes, options.MaxPageBytes)
			for chunkIndex, chunk := range chunks {
				blocks = append(blocks, cardEventBlock{
					text: chunk, pageBreak: event.PageBreak || chunkIndex > 0,
					responseStart: event.PageBreak && chunkIndex == 0,
					anchor:        cardChunkAnchor(event.ID, chunkIndex),
				})
			}
			continue
		}
		text = boundCardBlock(text, options.MaxPageRunes, options.MaxPageBytes)
		blocks = append(blocks, cardEventBlock{
			text: text, pageBreak: event.PageBreak, anchor: cardChunkAnchor(event.ID, 0),
		})
	}
	return blocks
}

func cardChunkAnchor(eventID string, chunk int) string {
	if eventID == "" {
		return ""
	}
	return fmt.Sprintf("%s.%d", eventID, chunk)
}

func renderCardEvent(event CardEvent, inFlight bool, options CardRenderOptions) string {
	switch event.Kind {
	case CardEventUserText:
		return "👤 " + truncateRunes(strings.TrimSpace(event.Text), 200)
	case CardEventAssistantText:
		return strings.TrimSpace(event.Text)
	case CardEventThinking:
		head := "∴ thinking" + cardTimeMarker(event, inFlight, options)
		return expandableCardBlock(
			head, event.Body, options.MaxPageRunes, options.MaxPageBytes, options.MaxTechnicalLines,
		)
	case CardEventToolCall:
		return renderToolCardEvent(event, inFlight, options)
	case CardEventToolResult:
		return renderToolCardEvent(event, false, options)
	default:
		return strings.TrimSpace(event.Text)
	}
}

func renderToolCardEvent(event CardEvent, inFlight bool, options CardRenderOptions) string {
	glyph := "✓"
	if event.IsError {
		glyph = "✗"
	} else if inFlight {
		glyph = "▷"
	}
	name := strings.TrimSpace(event.Text)
	if name == "" {
		name = strings.TrimSpace(event.ToolName)
	}
	if name == "" {
		name = "tool"
	}
	head := glyph + " " + truncateRunes(name, 120) + cardTimeMarker(event, inFlight, options)
	body := event.Body
	if event.HasResult {
		if body != "" && event.ResultBody != "" {
			body += "\n\n---\n\n" + event.ResultBody
		} else if event.ResultBody != "" {
			body = event.ResultBody
		}
	}
	return expandableCardBlock(
		head, body, options.MaxPageRunes, options.MaxPageBytes, options.MaxTechnicalLines,
	)
}

func cardTimeMarker(event CardEvent, inFlight bool, options CardRenderOptions) string {
	if event.StartedAt.IsZero() {
		return ""
	}
	if inFlight {
		elapsed := options.Now.Sub(event.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		seconds := int(elapsed / time.Second)
		return fmt.Sprintf(" · ⏳ %d:%02d", seconds/60, seconds%60)
	}
	return " · " + event.StartedAt.In(options.Location).Format("15:04")
}

func expandableCardBlock(head, body string, runeLimit, byteLimit, maxLines int) string {
	body = trimTechnicalBody(body, maxLines)
	if body == "" {
		return boundCardBlock(head, runeLimit, byteLimit)
	}
	head = truncateRunes(head, maxTechnicalSummary)
	escapedHead := html.EscapeString(head)
	body = escapeDetailsClose(body)
	prefix := "<details><summary>" + escapedHead + "</summary>\n\n"
	suffix := "\n\n</details>"
	availableRunes := runeLimit - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(suffix)
	availableBytes := byteLimit - len(prefix) - len(suffix)
	if availableRunes < 1 || availableBytes < 1 {
		return boundCardBlock(head, runeLimit, byteLimit)
	}
	body = truncateCardText(body, availableRunes, availableBytes)
	return prefix + body + suffix
}

func trimTechnicalBody(body string, maxLines int) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	contentLines := 0
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "```") {
			contentLines++
		}
	}
	if contentLines <= maxLines {
		return body
	}
	trimmed := make([]string, 0, maxLines+3)
	kept := 0
	fenceOpen := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			trimmed = append(trimmed, line)
			fenceOpen = !fenceOpen
			continue
		}
		if kept == maxLines {
			break
		}
		trimmed = append(trimmed, line)
		kept++
	}
	trimmed = append(trimmed, fmt.Sprintf("… (+%d more lines)", contentLines-maxLines))
	if fenceOpen {
		trimmed = append(trimmed, "```")
	}
	return strings.Join(trimmed, "\n")
}

func escapeDetailsClose(text string) string {
	return strings.ReplaceAll(text, "</details>", "&lt;/details&gt;")
}

func boundCardBlock(text string, runeLimit, byteLimit int) string {
	if utf8.RuneCountInString(text) <= runeLimit && len(text) <= byteLimit {
		return text
	}
	const suffix = "\n…"
	availableRunes := runeLimit - utf8.RuneCountInString(suffix)
	availableBytes := byteLimit - len(suffix)
	if availableRunes < 1 || availableBytes < 1 {
		return truncateCardText(text, runeLimit, byteLimit)
	}
	return truncateCardText(text, availableRunes, availableBytes) + suffix
}

func truncateCardText(text string, runeLimit, byteLimit int) string {
	if runeLimit <= 0 || byteLimit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) > runeLimit {
		runes = runes[:runeLimit]
	}
	runes = runes[:runePrefixWithinBytes(runes, byteLimit)]
	return string(runes)
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
