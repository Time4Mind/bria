package settings

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	FormatVersion     = 4
	DefaultQueueLimit = 32
	DefaultCardPages  = 64
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
	Version                   int               `json:"version"`
	ContinueExisting          bool              `json:"continue_existing"`
	ScreenEnabled             bool              `json:"screen_enabled"`
	CardDetail                CardDetail        `json:"card_detail"`
	CardPageLimit             int               `json:"card_page_limit"`
	ShowTechnicalActions      bool              `json:"show_technical_actions"`
	NotifyBackgroundQuestions bool              `json:"notify_background_questions"`
	NotifyBackgroundErrors    bool              `json:"notify_background_errors"`
	SessionLifetime           SessionLifetime   `json:"session_lifetime"`
	QueueLimit                int               `json:"queue_limit"`
	VoiceRecognition          VoiceRecognition  `json:"voice_recognition"`
	RetryUndeliveredFiles     bool              `json:"retry_undelivered_files"`
	ArchiveRecommendations    bool              `json:"archive_recommendations"`
	DefaultProviders          map[string]string `json:"default_providers"`
	DefaultWorkdirs           map[string]string `json:"default_workdirs"`
	PreprocessingEnabled      bool              `json:"preprocessing_enabled"`
	PreprocessingInstruction  string            `json:"preprocessing_instruction"`
}

type Effective struct {
	ContinueExisting           bool
	ScreenEnabled              bool
	CardDetail                 CardDetail
	CardPageLimit              int
	ShowTechnicalActions       bool
	NotifyBackgroundCompletion bool
	NotifyBackgroundQuestions  bool
	NotifyBackgroundErrors     bool
	SessionLifetime            SessionLifetime
	QueueLimit                 int
	VoiceRecognition           VoiceRecognition
	RetryUndeliveredFiles      bool
	ArchiveRecommendations     bool
	DefaultProviders           map[string]string
	DefaultWorkdirs            map[string]string
	PreprocessingEnabled       bool
	PreprocessingInstruction   string
}

func Default() Settings {
	return Settings{Version: FormatVersion, ContinueExisting: true, ScreenEnabled: false, CardDetail: CardDetailStandard, CardPageLimit: DefaultCardPages, ShowTechnicalActions: true, NotifyBackgroundQuestions: true, NotifyBackgroundErrors: true, SessionLifetime: Lifetime12Hours, QueueLimit: DefaultQueueLimit, VoiceRecognition: VoiceParakeet, RetryUndeliveredFiles: false, ArchiveRecommendations: false, DefaultProviders: map[string]string{}, DefaultWorkdirs: map[string]string{}}
}

func (s Settings) Effective() Effective {
	return Effective{
		ContinueExisting:           s.ContinueExisting,
		ScreenEnabled:              s.ScreenEnabled,
		CardDetail:                 s.CardDetail,
		CardPageLimit:              s.CardPageLimit,
		ShowTechnicalActions:       s.ShowTechnicalActions,
		NotifyBackgroundCompletion: true,
		NotifyBackgroundQuestions:  s.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:     s.NotifyBackgroundErrors,
		SessionLifetime:            s.SessionLifetime,
		QueueLimit:                 s.QueueLimit,
		VoiceRecognition:           s.VoiceRecognition,
		RetryUndeliveredFiles:      s.RetryUndeliveredFiles,
		ArchiveRecommendations:     s.ArchiveRecommendations,
		DefaultProviders:           cloneStringMap(s.DefaultProviders),
		DefaultWorkdirs:            cloneStringMap(s.DefaultWorkdirs),
		PreprocessingEnabled:       s.PreprocessingEnabled,
		PreprocessingInstruction:   s.PreprocessingInstruction,
	}
}

func (s Settings) Validate() error {
	if s.Version != FormatVersion {
		return fmt.Errorf("unsupported settings version %d", s.Version)
	}
	if s.CardDetail != CardDetailCompact && s.CardDetail != CardDetailStandard {
		return fmt.Errorf("unsupported card detail %q", s.CardDetail)
	}
	if s.CardPageLimit != 32 && s.CardPageLimit != 64 && s.CardPageLimit != 128 {
		return errors.New("card page limit must be 32, 64, or 128")
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
	for computerID, provider := range s.DefaultProviders {
		if !validMapKey(computerID) || provider != "codex" && provider != "claude" {
			return errors.New("default provider entries must contain a valid computer and provider")
		}
	}
	for computerID, workdir := range s.DefaultWorkdirs {
		if !validMapKey(computerID) || !portableAbsolute(workdir) || strings.TrimSpace(workdir) != workdir {
			return errors.New("default workdir entries must contain a valid computer and absolute path")
		}
	}
	if len(s.PreprocessingInstruction) > 16*1024 || strings.ContainsRune(s.PreprocessingInstruction, '\x00') ||
		s.PreprocessingInstruction != strings.TrimSpace(s.PreprocessingInstruction) {
		return errors.New("preprocessing instruction must be trimmed and at most 16384 bytes")
	}
	return nil
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func validMapKey(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func portableAbsolute(value string) bool {
	if value == "" || len(value) > 16*1024 || strings.ContainsRune(value, '\x00') {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
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
