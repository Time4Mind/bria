// Package settingscodec validates the JSON structure and versioned field
// presence of persisted settings. Value validation and migrations belong to settings.
package settingscodec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

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

// Inspect requires one object with known, unique top-level fields and the base
// required fields. It returns presence separately from values so omitted
// additive preferences can be distinguished from explicitly persisted false.
func Inspect(document []byte) (map[string]struct{}, error) {
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
	// Additive optional field: pre-feature documents keep the default OFF.
	allowed["standby_enabled"] = struct{}{}
	allowed["auto_approve_commands"] = struct{}{}
	allowed["screen_capture_limit_kib"] = struct{}{}
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

// RequireVersionFields checks fields introduced by each strict schema version.
// Versions 1 and 2 permit omissions for migration. Unsupported versions are
// rejected by the caller's settings value validation.
func RequireVersionFields(seen map[string]struct{}, version int) error {
	var fields []string
	switch version {
	case 3:
		fields = creationDocumentFields
	case 4:
		fields = append(creationDocumentFields, preprocessingDocumentFields...)
	case 5:
		fields = append(append(creationDocumentFields, preprocessingDocumentFields...), namingDocumentFields...)
	}
	for _, field := range fields {
		if _, ok := seen[field]; !ok {
			return fmt.Errorf("missing field %q", field)
		}
	}
	return nil
}
