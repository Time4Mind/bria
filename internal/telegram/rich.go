package telegram

import "bria/internal/telegramrich"

// NormalizeRichMarkdown applies the compact table layout expected by Telegram.
func NormalizeRichMarkdown(text string) string {
	return telegramrich.NormalizeRichMarkdown(text)
}
