package telegrambot

import "github.com/Time4Mind/bria/internal/telegramui"

// ParsePrivateDM rejects every update shape that does not carry exactly one
// message or callback from the same positive user/private-chat identity.
func ParsePrivateDM(update Update) (IncomingUpdate, error) {
	if update.UpdateID < 0 || (update.Message == nil) == (update.CallbackQuery == nil) {
		return IncomingUpdate{}, ErrNotPrivateDM
	}
	if update.Message != nil {
		message := update.Message
		if !allowsMessage(message) {
			return IncomingUpdate{}, ErrNotPrivateDM
		}
		content := message.Content
		if content.Kind == "" && message.Text != "" {
			content.Kind = IncomingText
		}
		if !validIncomingContent(content, message.Text) {
			return IncomingUpdate{}, ErrUnsupportedMessageContent
		}
		return IncomingUpdate{
			UpdateID:      update.UpdateID,
			Kind:          IncomingMessage,
			MessageDate:   message.Date,
			ChatID:        message.ChatID,
			UserID:        message.FromID,
			LanguageCode:  message.LanguageCode,
			MessageID:     message.MessageID,
			Text:          message.Text,
			Content:       content,
			Links:         message.Links,
			ForwardOrigin: message.ForwardOrigin,
			ViaBot:        message.ViaBot,
			Quote:         message.Quote,
			ExternalReply: message.ExternalReply,
		}, nil
	}

	callback := update.CallbackQuery
	if callback == nil || callback.ID == "" || callback.Data == "" ||
		len([]byte(callback.Data)) > telegramui.MaxCallbackBytes ||
		callback.Message == nil || callback.Message.MessageID <= 0 ||
		!allowsScope(callback.Message, callback.FromID) {
		return IncomingUpdate{}, ErrNotPrivateDM
	}
	message := callback.Message
	return IncomingUpdate{
		UpdateID:     update.UpdateID,
		Kind:         IncomingCallback,
		ChatID:       message.ChatID,
		UserID:       callback.FromID,
		LanguageCode: callback.FromLanguageCode,
		MessageID:    message.MessageID,
		CallbackID:   callback.ID,
		CallbackData: callback.Data,
		CallbackOrigin: Message{
			ChatID: message.ChatID, MessageID: message.MessageID, Text: message.Text,
			Rich: message.RichMessage, RichMediaFileID: message.RichMediaFileID,
		},
	}, nil
}

func validIncomingContent(content ContentDescriptor, text string) bool {
	if content.FileSize < 0 {
		return false
	}
	switch content.Kind {
	case IncomingText:
		return text != ""
	case IncomingVoice:
		return validFileID(content.FileID) && content.Duration >= 0
	case IncomingPhoto:
		return validFileID(content.FileID) && content.Width > 0 && content.Height > 0
	case IncomingDocument:
		return validFileID(content.FileID)
	default:
		return false
	}
}

func allowsMessage(message *UpdateMessage) bool {
	return message != nil && message.MessageID > 0 && allowsScope(message, message.FromID)
}

func allowsScope(message *UpdateMessage, userID int64) bool {
	return message != nil && telegramui.AllowsDM(telegramui.ChatScope{
		Kind:   telegramui.ChatKind(message.ChatType),
		ChatID: message.ChatID,
		UserID: userID,
	})
}
