package screenproduction

import (
	"context"

	"bria/internal/domain"
	"bria/internal/nativescreencache"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
)

// NativeImage identifies an immutable rendered PNG and its confirmed receipt.
type NativeImage = nativescreencache.NativeImage

// NativeSource renders a current cached native snapshot for the same rich card
// update. Cache state is independent of settings/provider adapters and has no
// cadence of its own; the Telegram mutation scheduler controls delivery timing.
type NativeSource = nativescreencache.Source

type ActiveSessionReader interface {
	LoadActiveSession(context.Context) (domain.SessionID, error)
}

// ScreenshotRefreshSource retains the legacy deferred-refresh capability.
// Production rich cards prefer NativeSource.CurrentScreenDelivery instead.
type ScreenshotRefreshSource interface {
	CachedScreenPNG(string) []byte
	RequestScreenPNG(context.Context, string) error
}

func NewNativeSource(preferences settings.Store, sessions ActiveSessionReader, runtime sessionruntime.NativeScreenProvider) (*NativeSource, error) {
	if preferences == nil || sessions == nil || runtime == nil {
		return nil, ErrInvalidConfiguration
	}
	return nativescreencache.New(nativescreencache.Config{
		Preferences: func(ctx context.Context) (nativescreencache.Preferences, error) {
			value, err := preferences.Load(ctx)
			return nativescreencache.Preferences{
				ScreenEnabled:         value.ScreenEnabled,
				ScreenCaptureLimitKiB: value.ScreenCaptureLimitKiB,
			}, err
		},
		ActiveSession: sessions.LoadActiveSession,
		Snapshot: func(id domain.SessionID) (nativescreencache.Snapshot, bool) {
			value, ok := runtime.NativeScreen(id)
			return nativescreencache.Snapshot{FullText: value.FullText, Hash: value.Hash}, ok
		},
	})
}
