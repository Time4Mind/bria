// Package promptpreprocess defines the isolated prompt-rewrite boundary and
// the durable envelope used before provider hand-off.
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

// Keep the v1 JSON shape readable by the immediately previous Bria binary.
// The durable routing decision is carried inside the already-supported
// instruction string so rollback does not reject pending input as unknown JSON.
const satelliteModeMarker = "\n\n[bria:satellite-mode="

const (
	maxInstructionBytes        = 16 * 1024
	maxEncodedInstructionBytes = maxInstructionBytes + len(satelliteModeMarker) + len(ModePerSession) + 1
)

const DefaultInstruction = "Преобразуй распознанную речь в короткий и понятный запрос. Удали нецензурную лексику, слова-паразиты, повторы и не относящийся к задаче речевой мусор. Исправь грамматику и только очевидные ошибки распознавания. Сохрани исходное намерение, все существенные условия, отрицания, имена, числа, даты и команды. Ничего не додумывай. Если исправление неоднозначно, оставь сомнительный фрагмент максимально близко к оригиналу. Верни только готовый запрос."

// Mode is the satellite routing decision durably fixed when input is admitted.
type Mode string

const (
	ModeDisabled   Mode = "disabled"
	ModeShared     Mode = "shared"
	ModePerSession Mode = "per_session"
)

type Request struct {
	ComputerID  domain.ComputerID
	SessionID   domain.SessionID
	MessageID   string
	Sequence    uint64
	Mode        Mode
	Instruction string
	Text        string
}

type Result struct {
	Text     string
	Provider domain.Provider
	Model    string
	// ModelEvidence identifies a provider-reported model receipt, not a price
	// ranking. Empty means Model is only the requested configuration.
	ModelEvidence string
	// Completion releases request-scoped satellite resources only after the
	// caller has durably accepted this result or its validated fallback. It does
	// not define the lifetime of the persistent satellite session itself.
	Completion Completion
}

type Completion interface {
	Accept(context.Context) error
}

type Processor interface {
	Process(context.Context, Request) (Result, error)
}

type Observation struct {
	ComputerID    domain.ComputerID
	SessionID     domain.SessionID
	MessageID     string
	Provider      domain.Provider
	Model         string
	ModelEvidence string
	Stage         string
	Category      string
	Attempts      int
	Error         string
}

type Observer interface {
	ObservePreprocessing(context.Context, Observation) error
}

type Invalidator interface {
	Invalidate(domain.ComputerID)
}

// ModeController applies the current durable satellite topology after a
// settings or provider change. Implementations must make repeated calls for
// the same mode restore the expected ready topology after invalidation.
type ModeController interface {
	SetMode(context.Context, Mode) error
}

type envelope struct {
	Instruction string `json:"instruction"`
	Text        string `json:"text"`
	Processed   string `json:"processed,omitempty"`
	Prepared    bool   `json:"prepared,omitempty"`
	Failed      bool   `json:"failed,omitempty"`
}

type DurableState struct {
	Mode        Mode
	Instruction string
	Original    string
	Processed   string
	Enabled     bool
	Prepared    bool
	Failed      bool
}

func Encode(instruction, text string) ([]byte, error) {
	return EncodeMode(ModeShared, instruction, text)
}

// EncodeMode fixes the selected routing mode in durable input. Disabled input
// remains raw so old consumers keep treating it as preprocessing-disabled.
func EncodeMode(mode Mode, instruction, text string) ([]byte, error) {
	instruction, text = strings.TrimSpace(instruction), strings.TrimSpace(text)
	if mode == ModeDisabled {
		if !validText(text) {
			return nil, errors.New("preprocessing envelope is invalid")
		}
		return []byte(text), nil
	}
	if instruction == "" {
		instruction = DefaultInstruction
	}
	encodedInstruction := instructionWithMode(instruction, mode)
	if !validEnabledMode(mode) || !validInstruction(instruction) || !validEncodedInstruction(encodedInstruction) || !validText(text) {
		return nil, errors.New("preprocessing envelope is invalid")
	}
	encoded, err := json.Marshal(envelope{Instruction: encodedInstruction, Text: text})
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
		return DurableState{Mode: ModeDisabled, Original: string(payload)}, nil
	}
	state := DurableState{Mode: ModeShared, Enabled: true}
	var value envelope
	decoder := json.NewDecoder(strings.NewReader(string(payload[len(envelopePrefix):])))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&value); decodeErr != nil || !validEncodedInstruction(value.Instruction) || !validText(value.Text) {
		return state, errors.New("preprocessing envelope is invalid")
	}
	mode, instruction := modeFromInstruction(value.Instruction)
	if !validEnabledMode(mode) || !validInstruction(instruction) {
		return state, errors.New("preprocessing envelope is invalid")
	}
	var trailing any
	if decodeErr := decoder.Decode(&trailing); !errors.Is(decodeErr, io.EOF) {
		return state, errors.New("preprocessing envelope is invalid")
	}
	if value.Prepared && !validText(value.Processed) || !value.Prepared && (value.Processed != "" || value.Failed) {
		return state, errors.New("preprocessing envelope is invalid")
	}
	return DurableState{Mode: mode, Instruction: instruction, Original: value.Text, Processed: value.Processed, Enabled: true, Prepared: value.Prepared, Failed: value.Failed}, nil
}

func MarkPrepared(payload []byte, processed string, failed bool) ([]byte, error) {
	state, err := DecodeState(payload)
	processed = strings.TrimSpace(processed)
	if err != nil || !state.Enabled || state.Prepared || !validText(processed) {
		return nil, errors.New("preprocessing envelope cannot be prepared")
	}
	encoded, err := json.Marshal(envelope{
		Instruction: instructionWithMode(state.Instruction, state.Mode), Text: state.Original,
		Processed: processed, Prepared: true, Failed: failed,
	})
	if err != nil {
		return nil, err
	}
	prepared := append([]byte(envelopePrefix), encoded...)
	if _, err := DecodeState(prepared); err != nil {
		return nil, errors.New("preprocessing envelope cannot be prepared")
	}
	return prepared, nil
}

func instructionWithMode(instruction string, mode Mode) string {
	return instruction + satelliteModeMarker + string(mode) + "]"
}

func modeFromInstruction(instruction string) (Mode, string) {
	for _, mode := range []Mode{ModeShared, ModePerSession} {
		marker := satelliteModeMarker + string(mode) + "]"
		if strings.HasSuffix(instruction, marker) {
			return mode, strings.TrimSuffix(instruction, marker)
		}
	}
	return ModeShared, instruction
}

func validEnabledMode(mode Mode) bool {
	return mode == ModeShared || mode == ModePerSession
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
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxInstructionBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validEncodedInstruction(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxEncodedInstructionBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 1<<20 && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
