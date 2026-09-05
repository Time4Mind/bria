// Package promptpreprocess defines the isolated stateless prompt-rewrite
// boundary and the durable envelope used before provider hand-off.
package promptpreprocess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"bria/internal/domain"
)

const envelopePrefix = "bria-preprocess-v1\n"

const DefaultInstruction = "Исправь ошибки распознавания и убери речевой мусор, не меняя намерение пользователя. Верни только готовый запрос без пояснений."

type Request struct {
	ComputerID  domain.ComputerID
	SessionID   domain.SessionID
	MessageID   string
	Instruction string
	Text        string
}

type Result struct {
	Text     string
	Provider domain.Provider
	Model    string
}

type Processor interface {
	Process(context.Context, Request) (Result, error)
}

type Observation struct {
	ComputerID domain.ComputerID
	SessionID  domain.SessionID
	MessageID  string
	Provider   domain.Provider
	Model      string
	Stage      string
	Category   string
	Attempts   int
	Error      string
}

type Observer interface {
	ObservePreprocessing(context.Context, Observation) error
}

type Invalidator interface {
	Invalidate(domain.ComputerID)
}

type envelope struct {
	Instruction string `json:"instruction"`
	Text        string `json:"text"`
	Processed   string `json:"processed,omitempty"`
	Prepared    bool   `json:"prepared,omitempty"`
	Failed      bool   `json:"failed,omitempty"`
}

type DurableState struct {
	Instruction string
	Original    string
	Processed   string
	Enabled     bool
	Prepared    bool
	Failed      bool
}

func Encode(instruction, text string) ([]byte, error) {
	instruction, text = strings.TrimSpace(instruction), strings.TrimSpace(text)
	if instruction == "" {
		instruction = DefaultInstruction
	}
	if !validInstruction(instruction) || !validText(text) {
		return nil, errors.New("preprocessing envelope is invalid")
	}
	encoded, err := json.Marshal(envelope{Instruction: instruction, Text: text})
	if err != nil {
		return nil, err
	}
	return append([]byte(envelopePrefix), encoded...), nil
}

func Decode(payload []byte) (instruction, text string, enabled bool, err error) {
	state, err := DecodeState(payload)
	if err != nil {
		return "", "", state.Enabled, err
	}
	text = state.Original
	if state.Prepared {
		text = state.Processed
	}
	return state.Instruction, text, state.Enabled, nil
}

func DecodeState(payload []byte) (DurableState, error) {
	if !strings.HasPrefix(string(payload), envelopePrefix) {
		return DurableState{Original: string(payload)}, nil
	}
	state := DurableState{Enabled: true}
	var value envelope
	decoder := json.NewDecoder(strings.NewReader(string(payload[len(envelopePrefix):])))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&value); decodeErr != nil || !validInstruction(value.Instruction) || !validText(value.Text) {
		return state, errors.New("preprocessing envelope is invalid")
	}
	var trailing any
	if decodeErr := decoder.Decode(&trailing); !errors.Is(decodeErr, io.EOF) {
		return state, errors.New("preprocessing envelope is invalid")
	}
	if value.Prepared && !validText(value.Processed) || !value.Prepared && (value.Processed != "" || value.Failed) {
		return state, errors.New("preprocessing envelope is invalid")
	}
	return DurableState{Instruction: value.Instruction, Original: value.Text, Processed: value.Processed, Enabled: true, Prepared: value.Prepared, Failed: value.Failed}, nil
}

func MarkPrepared(payload []byte, processed string, failed bool) ([]byte, error) {
	state, err := DecodeState(payload)
	processed = strings.TrimSpace(processed)
	if err != nil || !state.Enabled || state.Prepared || !validText(processed) {
		return nil, errors.New("preprocessing envelope cannot be prepared")
	}
	encoded, err := json.Marshal(envelope{
		Instruction: state.Instruction, Text: state.Original,
		Processed: processed, Prepared: true, Failed: failed,
	})
	if err != nil {
		return nil, err
	}
	return append([]byte(envelopePrefix), encoded...), nil
}

func Bypass(text string) bool {
	fields := strings.Fields(text)
	return len(fields) >= 1 && len(fields) <= 4 && strings.HasPrefix(fields[0], "/") && len(fields[0]) > 1
}

func ValidateResult(original, result string) error {
	original, result = strings.TrimSpace(original), strings.TrimSpace(result)
	if !validText(result) {
		return errors.New("preprocessing result is empty or invalid")
	}
	if toolish(result) {
		return errors.New("preprocessing result contains a tool call")
	}
	limit := max(len(original)*3, len(original)+1024)
	if limit > 1<<20 {
		limit = 1 << 20
	}
	if len(result) > limit {
		return errors.New("preprocessing result exceeds proportional limit")
	}
	return nil
}

func toolish(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{"<tool_call", "<tool_use", "<function_call", `"type":"tool_call"`, `"type": "tool_call"`, `"type":"tool_use"`, `"type": "tool_use"`, `"type":"function_call"`, `"type": "function_call"`} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func validInstruction(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 16*1024 && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 1<<20 && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
