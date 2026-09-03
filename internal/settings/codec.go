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

type settingsDocument struct {
	Version                   int              `json:"version"`
	Revision                  uint64           `json:"revision"`
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
	if err := inspectStrictDocument(document); err != nil {
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
	snapshot := decoded.snapshot()
	if err := snapshot.Settings.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("validate settings: %w", err)
	}
	return snapshot, nil
}

func inspectStrictDocument(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("settings must be a JSON object")
	}
	seen := make(map[string]struct{}, len(requiredDocumentFields))
	allowed := make(map[string]struct{}, len(requiredDocumentFields))
	for _, field := range requiredDocumentFields {
		allowed[field] = struct{}{}
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("settings field name must be a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = struct{}{}
		if _, known := allowed[key]; !known {
			return fmt.Errorf("unknown field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	for _, field := range requiredDocumentFields {
		if _, ok := seen[field]; !ok {
			return fmt.Errorf("missing field %q", field)
		}
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func documentFromSnapshot(snapshot Snapshot) settingsDocument {
	s := snapshot.Settings
	return settingsDocument{
		Version: s.Version, Revision: snapshot.Revision,
		ContinueExisting: s.ContinueExisting, ScreenEnabled: s.ScreenEnabled,
		CardDetail: s.CardDetail, ShowTechnicalActions: s.ShowTechnicalActions,
		NotifyBackgroundQuestions: s.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:    s.NotifyBackgroundErrors,
		SessionLifetime:           s.SessionLifetime, QueueLimit: s.QueueLimit,
		VoiceRecognition:      s.VoiceRecognition,
		RetryUndeliveredFiles: s.RetryUndeliveredFiles,
	}
}

func (document settingsDocument) snapshot() Snapshot {
	return Snapshot{Revision: document.Revision, Settings: Settings{
		Version:                   document.Version,
		ContinueExisting:          document.ContinueExisting,
		ScreenEnabled:             document.ScreenEnabled,
		CardDetail:                document.CardDetail,
		ShowTechnicalActions:      document.ShowTechnicalActions,
		NotifyBackgroundQuestions: document.NotifyBackgroundQuestions,
		NotifyBackgroundErrors:    document.NotifyBackgroundErrors,
		SessionLifetime:           document.SessionLifetime,
		QueueLimit:                document.QueueLimit,
		VoiceRecognition:          document.VoiceRecognition,
		RetryUndeliveredFiles:     document.RetryUndeliveredFiles,
	}}
}
