package settings

import (
	"context"
	"errors"
	"fmt"

	"bria/internal/settingsport"
)

const (
	FormatVersion     = 1
	DefaultQueueLimit = 32
)

type CardDetail string

const (
	CardDetailCompact  CardDetail = "compact"
	CardDetailStandard CardDetail = "standard"
)

type SessionLifetime string

const (
	LifetimeNever   SessionLifetime = "never"
	Lifetime6Hours  SessionLifetime = "6h"
	Lifetime12Hours SessionLifetime = "12h"
	Lifetime24Hours SessionLifetime = "24h"
	Lifetime48Hours SessionLifetime = "48h"
)

type VoiceRecognition string

const VoiceParakeet VoiceRecognition = "parakeet"

type Settings struct {
	Version                   int              `json:"version"`
	ContinueExisting          bool             `json:"continue_existing"`
	ScreenEnabled             bool             `json:"screen_enabled"`
	CardDetail                CardDetail       `json:"card_detail"`
	ShowTechnicalActions      bool             `json:"show_technical_actions"`
	NotifyBackgroundQuestions bool             `json:"notify_background_questions"`
	NotifyBackgroundErrors    bool             `json:"notify_background_errors"`
	SessionLifetime           SessionLifetime  `json:"session_lifetime"`
	QueueLimit                int              `json:"queue_limit"`
	VoiceRecognition          VoiceRecognition `json:"voice_recognition"`
	RetryUndeliveredFiles     bool             `json:"retry_undelivered_files"`
}

type Effective struct {
	ContinueExisting           bool
	ScreenEnabled              bool
	CardDetail                 CardDetail
	ShowTechnicalActions       bool
	NotifyBackgroundCompletion bool
	NotifyBackgroundQuestions  bool
	NotifyBackgroundErrors     bool
	SessionLifetime            SessionLifetime
	QueueLimit                 int
	VoiceRecognition           VoiceRecognition
	RetryUndeliveredFiles      bool
}

func Default() Settings {
	return Settings{Version: FormatVersion, ContinueExisting: true, ScreenEnabled: false, CardDetail: CardDetailStandard, ShowTechnicalActions: true, NotifyBackgroundQuestions: true, NotifyBackgroundErrors: true, SessionLifetime: LifetimeNever, QueueLimit: DefaultQueueLimit, VoiceRecognition: VoiceParakeet, RetryUndeliveredFiles: false}
}

func (s Settings) Effective() Effective {
	return Effective{
		ContinueExisting:           s.ContinueExisting,
		ScreenEnabled:              s.ScreenEnabled,
		CardDetail:                 s.CardDetail,
		ShowTechnicalActions:       s.ShowTechnicalActions,
		NotifyBackgroundCompletion: true,
		NotifyBackgroundQuestions:  s.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:     s.NotifyBackgroundErrors,
		SessionLifetime:            s.SessionLifetime,
		QueueLimit:                 s.QueueLimit,
		VoiceRecognition:           s.VoiceRecognition,
		RetryUndeliveredFiles:      s.RetryUndeliveredFiles,
	}
}

func (s Settings) Validate() error {
	if s.Version != FormatVersion {
		return fmt.Errorf("unsupported settings version %d", s.Version)
	}
	if s.CardDetail != CardDetailCompact && s.CardDetail != CardDetailStandard {
		return fmt.Errorf("unsupported card detail %q", s.CardDetail)
	}
	switch s.SessionLifetime {
	case LifetimeNever, Lifetime6Hours, Lifetime12Hours, Lifetime24Hours, Lifetime48Hours:
	default:
		return fmt.Errorf("unsupported session lifetime %q", s.SessionLifetime)
	}
	if s.QueueLimit < 1 || s.QueueLimit > 1024 {
		return errors.New("queue limit must be between 1 and 1024")
	}
	if s.VoiceRecognition != VoiceParakeet {
		return fmt.Errorf("unsupported voice recognition %q", s.VoiceRecognition)
	}
	return nil
}

type Store interface {
	Load(context.Context) (Settings, error)
	Update(context.Context, func(*Settings) error) error
}

// VersionedStore is the CAS seam for Telegram and local-file writers sharing
// one canonical settings document.
type VersionedStore interface {
	Store
	Current(context.Context) (Snapshot, error)
	CompareAndSwap(context.Context, uint64, Settings) (Snapshot, error)
}

type ReloadableStore interface {
	VersionedStore
	Reload(context.Context) (Snapshot, error)
	LastReloadError() error
}

type TelegramPreferences struct{ Store Store }

func NewTelegramPreferences(store Store) TelegramPreferences {
	return TelegramPreferences{Store: store}
}
func (p TelegramPreferences) Snapshot(ctx context.Context) (settingsport.Snapshot, error) {
	s := Default()
	if p.Store != nil {
		loaded, err := p.Store.Load(ctx)
		if err != nil {
			return settingsport.Snapshot{}, err
		}
		s = loaded
	}
	return settingsport.Snapshot{ContinueExisting: s.ContinueExisting, ScreenEnabled: s.ScreenEnabled, CardDetail: string(s.CardDetail), ShowTechnicalActions: s.ShowTechnicalActions, NotifyBackgroundQuestions: s.NotifyBackgroundQuestions, NotifyBackgroundErrors: s.NotifyBackgroundErrors, SessionLifetime: string(s.SessionLifetime), QueueLimit: s.QueueLimit, VoiceRecognition: string(s.VoiceRecognition)}, nil
}
func (p TelegramPreferences) update(ctx context.Context, f func(*Settings)) error {
	if p.Store == nil {
		return errors.New("settings store is required")
	}
	return p.Store.Update(ctx, func(s *Settings) error { f(s); return nil })
}
func (p TelegramPreferences) ToggleContinueExisting(ctx context.Context) error {
	return p.update(ctx, func(s *Settings) { s.ContinueExisting = !s.ContinueExisting })
}
func (p TelegramPreferences) ToggleScreen(ctx context.Context) error {
	return p.update(ctx, func(s *Settings) { s.ScreenEnabled = !s.ScreenEnabled })
}
func (p TelegramPreferences) ToggleCardDetail(ctx context.Context) error {
	return p.update(ctx, func(s *Settings) {
		if s.CardDetail == CardDetailStandard {
			s.CardDetail = CardDetailCompact
		} else {
			s.CardDetail = CardDetailStandard
		}
	})
}
func (p TelegramPreferences) ToggleTechnicalActions(ctx context.Context) error {
	return p.update(ctx, func(s *Settings) { s.ShowTechnicalActions = !s.ShowTechnicalActions })
}
func (p TelegramPreferences) ToggleBackgroundQuestions(ctx context.Context) error {
	return p.update(ctx, func(s *Settings) { s.NotifyBackgroundQuestions = !s.NotifyBackgroundQuestions })
}
func (p TelegramPreferences) ToggleBackgroundErrors(ctx context.Context) error {
	return p.update(ctx, func(s *Settings) { s.NotifyBackgroundErrors = !s.NotifyBackgroundErrors })
}
func (p TelegramPreferences) SetSessionLifetime(ctx context.Context, v string) error {
	return p.update(ctx, func(s *Settings) { s.SessionLifetime = SessionLifetime(v) })
}
