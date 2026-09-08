package telegramnotify

import "bria/internal/notificationstate"

// FilePartReceiptStore preserves the public notification store facade.
type FilePartReceiptStore = notificationstate.FilePartReceiptStore

func OpenFilePartReceiptStore(path string) (*FilePartReceiptStore, error) {
	return notificationstate.OpenFilePartReceiptStore(path)
}
