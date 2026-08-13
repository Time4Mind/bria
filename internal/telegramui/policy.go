package telegramui

type ChatKind string

const ChatPrivate ChatKind = "private"

type ChatScope struct {
	Kind   ChatKind
	ChatID int64
	UserID int64
}

// AllowsDM rejects groups, channels, malformed updates and private chats that
// do not belong to the effective user before callback parsing or projection.
func AllowsDM(scope ChatScope) bool {
	return scope.Kind == ChatPrivate &&
		scope.ChatID > 0 &&
		scope.UserID > 0 &&
		scope.ChatID == scope.UserID
}
