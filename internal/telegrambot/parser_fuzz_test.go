package telegrambot

import (
	"testing"
)

func FuzzUpdateJSONParserNeverAcceptsNonPrivateScope(f *testing.F) {
	f.Add(int64(1), int64(7), int64(7), "private", "ok")
	f.Add(int64(1), int64(7), int64(8), "group", "no")
	f.Fuzz(func(t *testing.T, updateID, userID, chatID int64, chatType, text string) {
		update := Update{UpdateID: updateID, Message: &UpdateMessage{
			MessageID: 1, FromID: userID, ChatID: chatID, ChatType: chatType,
			Text: text, Content: ContentDescriptor{Kind: IncomingText},
		}}
		incoming, err := ParsePrivateDM(update)
		if err == nil && (incoming.ChatID <= 0 || incoming.UserID <= 0 ||
			incoming.ChatID != incoming.UserID) {
			t.Fatalf("non-DM update accepted: %+v", incoming)
		}
	})
}
