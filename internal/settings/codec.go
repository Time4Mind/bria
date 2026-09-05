package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const MaxDocumentBytes = 64 << 10

var requiredDocumentFields = []string{
	"version",
	"revision",
	"continue_existing",
	"screen_enabled",
	"card_detail",
	"show_technical_actions",
	"notify_background_questions",
	"notify_background_errors",
	"session_lifetime",
	"queue_limit",
	"voice_recognition",
	"retry_undelivered_files",
}

var creationDocumentFields = []string{
	"archive_recommendations",
	"default_providers",
	"default_workdirs",
}

var preprocessingDocumentFields = []string{"preprocessing_enabled", "preprocessing_instruction"}
var namingDocumentFields = []string{"session_naming_enabled"}

type settingsDocument struct {
	Version                   int               `json:"version"`
	Revision                  uint64            `json:"revision"`
	ContinueExisting          bool              `json:"continue_existing"`
	ScreenEnabled             bool              `json:"screen_enabled"`
	CardDetail                CardDetail        `json:"card_detail"`
	CardPageLimit             int               `json:"card_page_limit,omitempty"`
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
	SessionNamingEnabled      bool              `json:"session_naming_enabled"`
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
	seen, err := inspectStrictDocument(document)
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
	} else if decoded.Version == 3 {
		for _, field := range creationDocumentFields {
			if _, ok := seen[field]; !ok {
				return Snapshot{}, fmt.Errorf("validate settings JSON: missing field %q", field)
			}
		}
		decoded.Version = FormatVersion
	} else if decoded.Version == 4 {
		for _, field := range append(creationDocumentFields, preprocessingDocumentFields...) {
			if _, ok := seen[field]; !ok {
				return Snapshot{}, fmt.Errorf("validate settings JSON: missing field %q", field)
			}
		}
		decoded.Version = FormatVersion
	} else if decoded.Version == FormatVersion {
		for _, field := range append(append(creationDocumentFields, preprocessingDocumentFields...), namingDocumentFields...) {
			if _, ok := seen[field]; !ok {
				return Snapshot{}, fmt.Errorf("validate settings JSON: missing field %q", field)
			}
		}
	}
	snapshot := decoded.snapshot()
	if err := snapshot.Settings.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("validate settings: %w", err)
	}
	return snapshot, nil
}

func inspectStrictDocument(document []byte) (map[string]struct{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("settings must be a JSON object")
	}
	seen := make(map[string]struct{}, len(requiredDocumentFields))
	allowed := make(map[string]struct{}, len(requiredDocumentFields))
	for _, field := range requiredDocumentFields {
		allowed[field] = struct{}{}
	}
	allowed["card_page_limit"] = struct{}{}
	for _, field := range creationDocumentFields {
		allowed[field] = struct{}{}
	}
	for _, field := range preprocessingDocumentFields {
		allowed[field] = struct{}{}
	}
	for _, field := range namingDocumentFields {
		allowed[field] = struct{}{}
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("settings field name must be a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = struct{}{}
		if _, known := allowed[key]; !known {
			return nil, fmt.Errorf("unknown field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	for _, field := range requiredDocumentFields {
		if _, ok := seen[field]; !ok {
			return nil, fmt.Errorf("missing field %q", field)
		}
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return seen, nil
}

func documentFromSnapshot(snapshot Snapshot) settingsDocument {
	s := snapshot.Settings
	return settingsDocument{
		Version: s.Version, Revision: snapshot.Revision,
		ContinueExisting: s.ContinueExisting, ScreenEnabled: s.ScreenEnabled,
		CardDetail: s.CardDetail, CardPageLimit: s.CardPageLimit, ShowTechnicalActions: s.ShowTechnicalActions,
		NotifyBackgroundQuestions: s.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:    s.NotifyBackgroundErrors,
		SessionLifetime:           s.SessionLifetime, QueueLimit: s.QueueLimit,
		VoiceRecognition:         s.VoiceRecognition,
		RetryUndeliveredFiles:    s.RetryUndeliveredFiles,
		ArchiveRecommendations:   s.ArchiveRecommendations,
		DefaultProviders:         cloneStringMap(s.DefaultProviders),
		DefaultWorkdirs:          cloneStringMap(s.DefaultWorkdirs),
		PreprocessingEnabled:     s.PreprocessingEnabled,
		PreprocessingInstruction: s.PreprocessingInstruction,
		SessionNamingEnabled:     s.SessionNamingEnabled,
	}
}

func (document settingsDocument) snapshot() Snapshot {
	return Snapshot{Revision: document.Revision, Settings: Settings{
		Version:                   document.Version,
		ContinueExisting:          document.ContinueExisting,
		ScreenEnabled:             document.ScreenEnabled,
		CardDetail:                document.CardDetail,
		CardPageLimit:             document.CardPageLimit,
		ShowTechnicalActions:      document.ShowTechnicalActions,
		NotifyBackgroundQuestions: document.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:    document.NotifyBackgroundErrors,
		SessionLifetime:           document.SessionLifetime,
		QueueLimit:                document.QueueLimit,
		VoiceRecognition:          document.VoiceRecognition,
		RetryUndeliveredFiles:     document.RetryUndeliveredFiles,
		ArchiveRecommendations:    document.ArchiveRecommendations,
		DefaultProviders:          cloneStringMap(document.DefaultProviders),
		DefaultWorkdirs:           cloneStringMap(document.DefaultWorkdirs),
		PreprocessingEnabled:      document.PreprocessingEnabled,
		PreprocessingInstruction:  document.PreprocessingInstruction,
		SessionNamingEnabled:      document.SessionNamingEnabled,
	}}
}
