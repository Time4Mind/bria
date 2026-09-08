// Package nativeacceptance validates the bounded atomic native acceptance
// document. It owns no files, processes, transcript interpretation or UI state.
package nativeacceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("native acceptance document invalid")

type Document struct {
	SessionID string            `json:"session_id"`
	Receipts  map[string]string `json:"receipts"`
	TurnIDs   map[string]string `json:"turn_ids,omitempty"`
}

func Decode(data []byte, sessionID string) (Document, error) {
	var doc Document
	if len(data) > 1<<20 || !utf8.Valid(data) {
		return doc, ErrInvalid
	}
	fields, err := object(data)
	if err != nil || len(fields) < 2 || len(fields) > 3 {
		return doc, ErrInvalid
	}
	for key, value := range fields {
		switch key {
		case "session_id":
			err = json.Unmarshal(value, &doc.SessionID)
		case "receipts":
			doc.Receipts, err = stringsObject(value)
		case "turn_ids":
			doc.TurnIDs, err = stringsObject(value)
		default:
			err = ErrInvalid
		}
		if err != nil {
			return Document{}, ErrInvalid
		}
	}
	if doc.SessionID != sessionID || validate(doc) != nil {
		return Document{}, ErrInvalid
	}
	return doc, nil
}

func Encode(doc Document) ([]byte, error) {
	if err := validate(doc); err != nil {
		return nil, err
	}
	data, err := json.Marshal(doc)
	if err != nil || len(data) > 1<<20 {
		return nil, ErrInvalid
	}
	return data, nil
}

// Accept returns a copy so a rejected or unpersisted change cannot alter the
// caller's existing receipt. A legacy uncorrelated identity cannot be guessed.
func Accept(doc Document, messageID, turnID string) (Document, error) {
	if validate(doc) != nil || !identity(messageID) || !identity(turnID) {
		return Document{}, ErrInvalid
	}
	if _, exists := doc.Receipts[messageID]; exists {
		if doc.TurnIDs[messageID] != turnID {
			return Document{}, ErrInvalid
		}
		return doc, nil
	}
	doc.Receipts, doc.TurnIDs = maps.Clone(doc.Receipts), maps.Clone(doc.TurnIDs)
	if doc.TurnIDs == nil {
		doc.TurnIDs = map[string]string{}
	}
	doc.Receipts[messageID], doc.TurnIDs[messageID] = "unknown", turnID
	if _, err := Encode(doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func validate(doc Document) error {
	if !identity(doc.SessionID) || doc.Receipts == nil || len(doc.Receipts) > 10000 {
		return ErrInvalid
	}
	for message, outcome := range doc.Receipts {
		if !identity(message) || outcome != "unknown" && outcome != "completed" && outcome != "failed" {
			return ErrInvalid
		}
	}
	for message, turn := range doc.TurnIDs {
		if _, ok := doc.Receipts[message]; !ok || !identity(turn) {
			return ErrInvalid
		}
	}
	return nil
}

func identity(value string) bool {
	return value != "" && len(value) <= 1024 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func stringsObject(data []byte) (map[string]string, error) {
	fields, err := object(data)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(fields))
	for key, raw := range fields {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return nil, ErrInvalid
		}
		result[key] = value
	}
	return result, nil
}

func object(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	result := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || result[name] != nil || len(result) >= 10000 {
			return nil, ErrInvalid
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, ErrInvalid
		}
		result[name] = raw
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return result, nil
}
