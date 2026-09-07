package screen

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	DefaultNativeCaptureKiB = 48
	NativeMaxPNGBytes       = 1 << 20
)

var ErrInvalidNativeOptions = errors.New("invalid native screen options")

// NativeOptions controls only the bounded terminal payload. CaptureKiB is
// intentionally limited to the three owner-approved settings values.
type NativeOptions struct {
	CaptureKiB int
}

// RenderNative rasterizes an explicitly selected native terminal capture. It
// does not read a terminal or build a fake terminal from provider events. Raw
// content is bounded, never logged or persisted, and returned only as a PNG.
func RenderNative(ctx context.Context, text string) ([]byte, error) {
	return RenderNativeWithOptions(ctx, text, NativeOptions{})
}

// RenderNativeWithOptions rasterizes the newest bounded terminal payload. If
// the encoded PNG exceeds Telegram's rich-photo limit, the oldest rendered
// rows are removed until the newest useful payload fits.
func RenderNativeWithOptions(ctx context.Context, text string, options NativeOptions) ([]byte, error) {
	if ctx == nil || !utf8.ValidString(text) {
		return nil, ErrEventTooLarge
	}
	limits, err := nativeLimitsFor(options)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = trimNativeCapture(text, limits.captureBytes)
	png, _, _, err := renderNativeANSI(strings.TrimRight(text, "\n"), limits)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	return png, err
}

func trimNativeCapture(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	// Prefer a complete terminal row. This also prevents a raw byte cut from
	// exposing the tail of a CSI/OSC sequence as printable screenshot text.
	if newline := strings.IndexByte(text[start:], '\n'); newline >= 0 {
		start += newline + 1
	}
	return text[start:]
}
