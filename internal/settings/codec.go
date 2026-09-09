package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"bria/internal/settingscodec"
)

const MaxDocumentBytes = 64 << 10

// ShowHiddenDirectories is additive v5: absence means OFF; older strict binaries
// reject the new field.
type settingsDocument struct {
	Version                   int               `json:"version"`
	Revision                  uint64            `json:"revision"`
	ContinueExisting          bool              `json:"continue_existing"`
	ScreenEnabled             bool              `json:"screen_enabled"`
	ScreenCaptureLimitKiB     int               `json:"screen_capture_limit_kib,omitempty"`
	CardDetail                CardDetail        `json:"card_detail"`
	CardPageLimit             int               `json:"card_page_limit,omitempty"`
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
	ShowHiddenDirectories     bool              `json:"show_hidden_directories"`
	DefaultProviders          map[string]string `json:"default_providers"`
	DefaultWorkdirs           map[string]string `json:"default_workdirs"`
	PreprocessingEnabled      bool              `json:"preprocessing_enabled"`
	PreprocessingInstruction  string            `json:"preprocessing_instruction"`
	SessionNamingEnabled      bool              `json:"session_naming_enabled"`
	StandbyEnabled            bool              `json:"standby_enabled"`
	AutoApproveCommands       bool              `json:"auto_approve_commands"`
}

// Decode reads one complete settings document. Every field is explicit so a
// malformed local edit cannot silently reset a boolean or numeric preference.
func Decode(reader io.Reader) (Snapshot, error) {
	if reader == nil {
		return Snapshot{}, errors.New("settings reader is required")
	}
	document, err := io.ReadAll(io.LimitReader(reader, MaxDocumentBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read settings: %w", err)
	}
	if len(document) > MaxDocumentBytes {
		return Snapshot{}, fmt.Errorf("settings exceed %d bytes", MaxDocumentBytes)
	}
	if !utf8.Valid(document) {
		return Snapshot{}, errors.New("settings must be valid UTF-8")
	}
	seen, err := settingscodec.Inspect(document)
	if err != nil {
		return Snapshot{}, fmt.Errorf("validate settings JSON: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var decoded settingsDocument
	if err := decoder.Decode(&decoded); err != nil {
		return Snapshot{}, fmt.Errorf("decode settings: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Snapshot{}, errors.New("decode settings: trailing JSON value")
		}
		return Snapshot{}, fmt.Errorf("decode settings trailing data: %w", err)
	}
	if decoded.Revision == 0 {
		return Snapshot{}, errors.New("settings revision must be positive")
	}
	if err := settingscodec.RequireVersionFields(seen, decoded.Version); err != nil {
		return Snapshot{}, fmt.Errorf("validate settings JSON: %w", err)
	}
	if decoded.Version >= 1 && decoded.Version < 3 {
		decoded.Version = FormatVersion
		if decoded.CardPageLimit == 0 {
			decoded.CardPageLimit = DefaultCardPages
		}
		if decoded.DefaultProviders == nil {
			decoded.DefaultProviders = map[string]string{}
		}
		if decoded.DefaultWorkdirs == nil {
			decoded.DefaultWorkdirs = map[string]string{}
		}
	} else if decoded.Version == 3 || decoded.Version == 4 {
		decoded.Version = FormatVersion
	}
	if _, ok := seen["auto_approve_commands"]; !ok {
		decoded.AutoApproveCommands = true
	}
	// Additive v5 preference: old documents retain the default. A pre-feature
	// strict codec cannot read documents written with this field; binary-only
	// rollback therefore requires a separately prepared compatible document.
	if _, ok := seen["technical_output_lines"]; !ok {
		decoded.TechnicalOutputLines = DefaultTechnicalOutputLines
	}
	// Only documents without the separate command preference are legacy.
	if _, ok := seen["technical_command_lines"]; !ok {
		if decoded.TechnicalOutputLines == 40 {
			decoded.TechnicalOutputLines = 20
		}
		decoded.TechnicalCommandLines = decoded.TechnicalOutputLines
	}
	snapshot := decoded.snapshot()
	if snapshot.Settings.ScreenCaptureLimitKiB == 0 {
		snapshot.Settings.ScreenCaptureLimitKiB = DefaultScreenCaptureLimitKiB
	}
	if err := snapshot.Settings.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("validate settings: %w", err)
	}
	return snapshot, nil
}

func documentFromSnapshot(snapshot Snapshot) settingsDocument {
	s := snapshot.Settings
	return settingsDocument{
		Version: s.Version, Revision: snapshot.Revision,
		ContinueExisting: s.ContinueExisting, ScreenEnabled: s.ScreenEnabled, ScreenCaptureLimitKiB: s.ScreenCaptureLimitKiB,
		CardDetail: s.CardDetail, CardPageLimit: s.CardPageLimit, ShowTechnicalActions: s.ShowTechnicalActions,
		TechnicalOutputLines:      s.TechnicalOutputLines,
		TechnicalCommandLines:     s.TechnicalCommandLines,
		NotifyBackgroundQuestions: s.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:    s.NotifyBackgroundErrors,
		SessionLifetime:           s.SessionLifetime, QueueLimit: s.QueueLimit,
		VoiceRecognition:         s.VoiceRecognition,
		RetryUndeliveredFiles:    s.RetryUndeliveredFiles,
		ArchiveRecommendations:   s.ArchiveRecommendations,
		ShowHiddenDirectories:    s.ShowHiddenDirectories,
		DefaultProviders:         cloneStringMap(s.DefaultProviders),
		DefaultWorkdirs:          cloneStringMap(s.DefaultWorkdirs),
		PreprocessingEnabled:     s.PreprocessingEnabled,
		PreprocessingInstruction: s.PreprocessingInstruction,
		SessionNamingEnabled:     s.SessionNamingEnabled,
		StandbyEnabled:           s.StandbyEnabled,
		AutoApproveCommands:      s.AutoApproveCommands,
	}
}

func (document settingsDocument) snapshot() Snapshot {
	return Snapshot{Revision: document.Revision, Settings: Settings{
		Version:                   document.Version,
		ContinueExisting:          document.ContinueExisting,
		ScreenEnabled:             document.ScreenEnabled,
		ScreenCaptureLimitKiB:     document.ScreenCaptureLimitKiB,
		CardDetail:                document.CardDetail,
		CardPageLimit:             document.CardPageLimit,
		ShowTechnicalActions:      document.ShowTechnicalActions,
		TechnicalOutputLines:      document.TechnicalOutputLines,
		TechnicalCommandLines:     document.TechnicalCommandLines,
		NotifyBackgroundQuestions: document.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:    document.NotifyBackgroundErrors,
		SessionLifetime:           document.SessionLifetime,
		QueueLimit:                document.QueueLimit,
		VoiceRecognition:          document.VoiceRecognition,
		RetryUndeliveredFiles:     document.RetryUndeliveredFiles,
		ArchiveRecommendations:    document.ArchiveRecommendations,
		ShowHiddenDirectories:     document.ShowHiddenDirectories,
		DefaultProviders:          cloneStringMap(document.DefaultProviders),
		DefaultWorkdirs:           cloneStringMap(document.DefaultWorkdirs),
		PreprocessingEnabled:      document.PreprocessingEnabled,
		PreprocessingInstruction:  document.PreprocessingInstruction,
		SessionNamingEnabled:      document.SessionNamingEnabled,
		StandbyEnabled:            document.StandbyEnabled,
		AutoApproveCommands:       document.AutoApproveCommands,
	}}
}
