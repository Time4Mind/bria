package telegrambridge

import (
	"context"
	"errors"
	"strings"

	"bria/internal/coordinator"
	"bria/internal/telegram"
)

type ScreenSource interface {
	ScreenPNG(context.Context, string) ([]byte, error)
}

type cachedScreenSource interface {
	CachedScreenDelivery(string) ([]byte, string, string)
	RequestScreenPNG(context.Context, string) error
	RememberTelegramFileID(string, string, string) bool
}

type deferredScreenSource interface {
	CachedScreenPNG(string) []byte
	RequestScreenPNG(context.Context, string) error
}

type screenReceipt struct {
	sessionID string
	pngHash   string
}

// BindScreenSource is startup-only. Source must enforce active session and
// global opt-in before returning any native terminal image bytes.
func (sender *Sender) BindScreenSource(source any) error {
	if sender == nil || source == nil || sender.screen != nil {
		return errors.New("screen source binding is invalid")
	}
	if _, ok := source.(ScreenSource); !ok {
		if _, cached := source.(cachedScreenSource); !cached {
			if _, deferred := source.(deferredScreenSource); !deferred {
				return errors.New("screen source binding is invalid")
			}
		}
	}
	sender.screen = source
	return nil
}

func (sender *Sender) screenMessage(ctx context.Context, status coordinator.Status) (telegram.InputRichMessage, []byte, screenReceipt, error) {
	rich := telegram.InputRichMessage{Markdown: telegram.NormalizeRichMarkdown(status.Text)}
	if sender.screen == nil || status.ScreenSessionID == "" {
		return rich, nil, screenReceipt{}, nil
	}
	// Native production screenshots are refreshed asynchronously. The card
	// must use only the last completed image; text and keyboard delivery never
	// wait for capture or PNG encoding. The refresh is intentionally requested
	// after reading the cache so this rich_md update carries the previous ready
	// image while the next one is prepared.
	if deferred, ok := sender.screen.(cachedScreenSource); ok {
		data, pngHash, fileID := deferred.CachedScreenDelivery(status.ScreenSessionID)
		_ = deferred.RequestScreenPNG(context.WithoutCancel(ctx), status.ScreenSessionID)
		rich, data = screenRichMessage(rich, data, fileID)
		receipt := screenReceipt{}
		if len(data) != 0 && pngHash != "" {
			receipt = screenReceipt{sessionID: status.ScreenSessionID, pngHash: pngHash}
		}
		return rich, data, receipt, nil
	}
	if deferred, ok := sender.screen.(deferredScreenSource); ok {
		data := deferred.CachedScreenPNG(status.ScreenSessionID)
		go func() { _ = deferred.RequestScreenPNG(context.WithoutCancel(ctx), status.ScreenSessionID) }()
		rich, data = screenRichMessage(rich, data, "")
		return rich, data, screenReceipt{}, nil
	}
	source, ok := sender.screen.(ScreenSource)
	if !ok {
		return rich, nil, screenReceipt{}, errors.New("screen source is not readable")
	}
	data, err := source.ScreenPNG(ctx, status.ScreenSessionID)
	if err != nil {
		return rich, nil, screenReceipt{}, err
	}
	rich, data = screenRichMessage(rich, data, "")
	return rich, data, screenReceipt{}, nil
}

func screenRichMessage(rich telegram.InputRichMessage, data []byte, fileID string) (telegram.InputRichMessage, []byte) {
	if len(data) > telegram.MaxRichPhotoBytes {
		return rich, nil
	}
	reference := ""
	if fileID != "" {
		reference = fileID
		data = nil
	} else if len(data) != 0 {
		reference = "attach://" + telegram.ScreenPhotoID
	}
	if reference == "" {
		return rich, nil
	}
	rich.Markdown = strings.TrimRight(rich.Markdown, "\n") + "\n\n\u00a0\n\n![](tg://photo?id=" + telegram.ScreenPhotoID + ")"
	rich.Media = []telegram.InputRichMessageMedia{{ID: telegram.ScreenPhotoID, Media: telegram.InputRichMediaPhoto{Type: "photo", Media: reference}}}
	return rich, data
}

func (sender *Sender) rememberScreenReceipt(receipt screenReceipt, message telegram.Message) {
	if receipt.sessionID == "" || receipt.pngHash == "" || sender == nil || sender.screen == nil {
		return
	}
	source, ok := sender.screen.(cachedScreenSource)
	if !ok {
		return
	}
	if fileID := telegram.RichPhotoFileID(message); fileID != "" {
		source.RememberTelegramFileID(receipt.sessionID, receipt.pngHash, fileID)
	}
}
