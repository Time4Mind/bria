package settings

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	FormatVersion                = 5
	DefaultQueueLimit            = 32
	DefaultCardPages             = 64
	DefaultTechnicalOutputLines  = 10
	DefaultTechnicalCommandLines = 10
	DefaultScreenCaptureLimitKiB = 48
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
	ScreenCaptureLimitKiB     int               `json:"screen_capture_limit_kib"`
	CardDetail                CardDetail        `json:"card_detail"`
	CardPageLimit             int               `json:"card_page_limit"`
	ShowTechnicalActions      bool              `json:"show_technical_actions"`
	TechnicalOutputLines      int               `json:"technical_output_lines"`
	TechnicalCommandLines     int               `json:"technical_command_lines"`
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
	SessionNamingEnabled      bool              `json:"session_naming_enabled"`
	StandbyEnabled            bool              `json:"standby_enabled"`
	AutoApproveCommands       bool              `json:"auto_approve_commands"`
}

type Effective struct {
	ContinueExisting           bool
	ScreenEnabled              bool
	ScreenCaptureLimitKiB      int
	CardDetail                 CardDetail
	CardPageLimit              int
	ShowTechnicalActions       bool
	TechnicalOutputLines       int
	TechnicalCommandLines      int
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
	SessionNamingEnabled       bool
	StandbyEnabled             bool
	AutoApproveCommands        bool
}

func Default() Settings {
	return Settings{Version: FormatVersion, ContinueExisting: true, ScreenEnabled: false, ScreenCaptureLimitKiB: DefaultScreenCaptureLimitKiB, CardDetail: CardDetailStandard, CardPageLimit: DefaultCardPages, ShowTechnicalActions: true, TechnicalOutputLines: DefaultTechnicalOutputLines, TechnicalCommandLines: DefaultTechnicalCommandLines, NotifyBackgroundQuestions: false, NotifyBackgroundErrors: true, SessionLifetime: Lifetime12Hours, QueueLimit: DefaultQueueLimit, VoiceRecognition: VoiceParakeet, RetryUndeliveredFiles: false, ArchiveRecommendations: false, DefaultProviders: map[string]string{}, DefaultWorkdirs: map[string]string{}, AutoApproveCommands: true}
}

func (s Settings) Effective() Effective {
	return Effective{
		ContinueExisting:           s.ContinueExisting,
		ScreenEnabled:              s.ScreenEnabled,
		ScreenCaptureLimitKiB:      s.ScreenCaptureLimitKiB,
		CardDetail:                 s.CardDetail,
		CardPageLimit:              s.CardPageLimit,
		ShowTechnicalActions:       s.ShowTechnicalActions,
		TechnicalOutputLines:       s.TechnicalOutputLines,
		TechnicalCommandLines:      s.TechnicalCommandLines,
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
		SessionNamingEnabled:       s.SessionNamingEnabled,
		StandbyEnabled:             s.StandbyEnabled,
		AutoApproveCommands:        s.AutoApproveCommands,
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
	for _, limit := range []int{s.TechnicalCommandLines, s.TechnicalOutputLines} {
		if limit != 3 && limit != 5 && limit != 10 && limit != 20 {
			return errors.New("technical command and output lines must each be 3, 5, 10, or 20")
		}
	}
	if s.ScreenCaptureLimitKiB != 48 && s.ScreenCaptureLimitKiB != 64 && s.ScreenCaptureLimitKiB != 86 {
		return errors.New("screen capture limit must be 48, 64, or 86 KiB")
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
