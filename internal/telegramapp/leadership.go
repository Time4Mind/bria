package telegramapp

// Leadership reports whether this process may currently handle Telegram work.
type Leadership interface {
	IsLeader() bool
}
