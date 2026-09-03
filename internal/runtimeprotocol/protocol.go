// Package runtimeprotocol defines Bria's provider-neutral, bounded JSONL wire
// contract between the session runtime and provider adapters.
package runtimeprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// EnvironmentProviderCredentialFile is the provider-neutral adapter startup
// reference to a private local credential file. It must never contain the
// credential itself. Each adapter owns interpretation of the referenced file.
const EnvironmentProviderCredentialFile = "BRIA_PROVIDER_CREDENTIAL_FILE"

const Version = 1

const (
	DefaultMaxLineBytes          = 64 * 1024
	DefaultMaxTextBytes          = 32 * 1024
	DefaultMaxAttachmentBytes    = 32 * 1024 * 1024
	DefaultMaxQuestions          = 8
	DefaultMaxOptionsPerQuestion = 8
	DefaultMaxAnswersPerQuestion = 8
)

var ErrProtocol = errors.New("runtime protocol violation")

type MessageType string

const (
	TypeSubmit                      MessageType = "submit"
	TypeInterrupt                   MessageType = "interrupt"
	TypeClose                       MessageType = "close"
	TypeInteractionResponse         MessageType = "interaction_response"
	TypeReconcileAcceptedTurns      MessageType = "reconcile_accepted_turns"
	TypeReady                       MessageType = "ready"
	TypeAccepted                    MessageType = "accepted"
	TypeEvent                       MessageType = "event"
	TypeFinal                       MessageType = "final"
	TypeCompleted                   MessageType = "completed"
	TypeInteractionRequest          MessageType = "interaction_request"
	TypeInteractionResponseAccepted MessageType = "interaction_response_accepted"
	TypeAcceptedTurn                MessageType = "accepted_turn"
	TypeReconciliationCompleted     MessageType = "reconciliation_completed"
)

type InteractionKind string

const (
	InteractionQuestion        InteractionKind = "question"
	InteractionCommandApproval InteractionKind = "command_approval"
	InteractionFileApproval    InteractionKind = "file_approval"
)

type InteractionOutcome string

const (
	OutcomeAnswered  InteractionOutcome = "answered"
	OutcomeCancelled InteractionOutcome = "cancelled"
)

type ApprovalDecision string

const (
	DecisionAccept           ApprovalDecision = "accept"
	DecisionAcceptForSession ApprovalDecision = "acceptForSession"
	DecisionDecline          ApprovalDecision = "decline"
	DecisionCancel           ApprovalDecision = "cancel"
)

type Limits struct {
	MaxLineBytes          int
	MaxTextBytes          int
	MaxQuestions          int
	MaxOptionsPerQuestion int
	MaxAnswersPerQuestion int
}

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type Question struct {
	ID       string   `json:"id"`
	Header   string   `json:"header"`
	Text     string   `json:"text"`
	Options  []Option `json:"options,omitempty"`
	IsOther  bool     `json:"is_other,omitempty"`
	IsSecret bool     `json:"is_secret,omitempty"`
}

type InteractionRequest struct {
	ID          string             `json:"id"`
	Kind        InteractionKind    `json:"kind"`
	ThreadID    string             `json:"thread_id,omitempty"`
	TurnID      string             `json:"turn_id,omitempty"`
	ItemID      string             `json:"item_id"`
	Blocking    bool               `json:"blocking,omitempty"`
	Questions   []Question         `json:"questions,omitempty"`
	ApprovalID  string             `json:"approval_id,omitempty"`
	StartedAtMS int64              `json:"started_at_ms,omitempty"`
	Reason      string             `json:"reason,omitempty"`
	Command     string             `json:"command,omitempty"`
	Cwd         string             `json:"cwd,omitempty"`
	GrantRoot   string             `json:"grant_root,omitempty"`
	Decisions   []ApprovalDecision `json:"decisions,omitempty"`
}

type InteractionResponse struct {
	ID       string              `json:"id"`
	Outcome  InteractionOutcome  `json:"outcome"`
	Answers  map[string][]string `json:"answers,omitempty"`
	Decision ApprovalDecision    `json:"decision,omitempty"`
}

type LocalAttachment struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type ParentMessage struct {
	Protocol            int                  `json:"protocol"`
	Type                MessageType          `json:"type"`
	RequestID           string               `json:"request_id,omitempty"`
	Text                string               `json:"text,omitempty"`
	MessageID           string               `json:"message_id,omitempty"`
	Attachments         []LocalAttachment    `json:"attachments,omitempty"`
	InteractionResponse *InteractionResponse `json:"interaction_response,omitempty"`
}

type AdapterMessage struct {
	Protocol           int                 `json:"protocol"`
	Type               MessageType         `json:"type"`
	ProviderSessionID  string              `json:"provider_session_id,omitempty"`
	Readiness          string              `json:"readiness,omitempty"`
	Authentication     string              `json:"authentication,omitempty"`
	RequestID          string              `json:"request_id,omitempty"`
	MessageID          string              `json:"message_id,omitempty"`
	Kind               string              `json:"kind,omitempty"`
	Text               string              `json:"text,omitempty"`
	Status             string              `json:"status,omitempty"`
	ErrorCode          string              `json:"error_code,omitempty"`
	InteractionRequest *InteractionRequest `json:"interaction_request,omitempty"`
	InteractionID      string              `json:"interaction_id,omitempty"`
}

func DecodeParentLine(line []byte, limits Limits) (ParentMessage, error) {
	limits, err := effectiveLimits(limits)
	if err != nil || !validLineSize(line, limits.MaxLineBytes) || rejectDuplicateKeys(line) != nil {
		return ParentMessage{}, ErrProtocol
	}
	var message ParentMessage
	if decodeStrict(line, &message) != nil || validateFieldSet(line, parentFields(message.Type)) != nil ||
		validateParent(message, limits) != nil {
		return ParentMessage{}, ErrProtocol
	}
	return message, nil
}

func EncodeParentLine(message ParentMessage, limits Limits) ([]byte, error) {
	limits, err := effectiveLimits(limits)
	if err != nil || validateParent(message, limits) != nil {
		return nil, ErrProtocol
	}
	return encodeParentLine(message, limits.MaxLineBytes)
}

func DecodeAdapterLine(line []byte, limits Limits) (AdapterMessage, error) {
	limits, err := effectiveLimits(limits)
	if err != nil || !validLineSize(line, limits.MaxLineBytes) || rejectDuplicateKeys(line) != nil {
		return AdapterMessage{}, ErrProtocol
	}
	var message AdapterMessage
	if decodeStrict(line, &message) != nil || validateFieldSet(line, adapterFields(message.Type)) != nil ||
		validateAdapter(message, limits) != nil {
		return AdapterMessage{}, ErrProtocol
	}
	return message, nil
}

func EncodeAdapterLine(message AdapterMessage, limits Limits) ([]byte, error) {
	limits, err := effectiveLimits(limits)
	if err != nil || validateAdapter(message, limits) != nil {
		return nil, ErrProtocol
	}
	return encodeAdapterLine(message, limits.MaxLineBytes)
}

// ValidateResponse proves that a response belongs to one exact interaction
// and contains only answers or a decision advertised by that request.
func ValidateResponse(request InteractionRequest, response InteractionResponse, limits Limits) error {
	limits, err := effectiveLimits(limits)
	if err != nil || validateInteractionRequest(request, limits) != nil ||
		validateInteractionResponse(response, limits) != nil || request.ID != response.ID {
		return ErrProtocol
	}
	if response.Outcome == OutcomeCancelled {
		return nil
	}
	switch request.Kind {
	case InteractionQuestion:
		if response.Decision != "" || len(response.Answers) != len(request.Questions) {
			return ErrProtocol
		}
		questions := make(map[string]Question, len(request.Questions))
		for _, question := range request.Questions {
			questions[question.ID] = question
		}
		for questionID, answers := range response.Answers {
			question, ok := questions[questionID]
			if !ok || len(answers) == 0 || len(answers) > limits.MaxAnswersPerQuestion {
				return ErrProtocol
			}
			seen := make(map[string]struct{}, len(answers))
			for _, answer := range answers {
				if !validRequiredText(answer, limits.MaxTextBytes) {
					return ErrProtocol
				}
				if _, duplicate := seen[answer]; duplicate {
					return ErrProtocol
				}
				seen[answer] = struct{}{}
				if len(question.Options) > 0 && !question.IsOther && !hasOption(question.Options, answer) {
					return ErrProtocol
				}
			}
		}
		return nil
	case InteractionCommandApproval, InteractionFileApproval:
		if response.Answers != nil || !containsDecision(request.Decisions, response.Decision) {
			return ErrProtocol
		}
		return nil
	default:
		return ErrProtocol
	}
}

// ValidateInteractionExchange validates both the active turn correlation and
// the provider interaction correlation of one request-response pair.
func ValidateInteractionExchange(request AdapterMessage, response ParentMessage, limits Limits) error {
	limits, err := effectiveLimits(limits)
	if err != nil || validateAdapter(request, limits) != nil || validateParent(response, limits) != nil ||
		request.Type != TypeInteractionRequest || response.Type != TypeInteractionResponse ||
		request.RequestID != response.RequestID || request.InteractionRequest == nil || response.InteractionResponse == nil {
		return ErrProtocol
	}
	return ValidateResponse(*request.InteractionRequest, *response.InteractionResponse, limits)
}

func effectiveLimits(limits Limits) (Limits, error) {
	if limits.MaxLineBytes == 0 {
		limits.MaxLineBytes = DefaultMaxLineBytes
	}
	if limits.MaxTextBytes == 0 {
		limits.MaxTextBytes = DefaultMaxTextBytes
	}
	if limits.MaxQuestions == 0 {
		limits.MaxQuestions = DefaultMaxQuestions
	}
	if limits.MaxOptionsPerQuestion == 0 {
		limits.MaxOptionsPerQuestion = DefaultMaxOptionsPerQuestion
	}
	if limits.MaxAnswersPerQuestion == 0 {
		limits.MaxAnswersPerQuestion = DefaultMaxAnswersPerQuestion
	}
	if limits.MaxLineBytes < 128 || limits.MaxTextBytes < 1 || limits.MaxQuestions < 1 ||
		limits.MaxOptionsPerQuestion < 1 || limits.MaxAnswersPerQuestion < 1 {
		return Limits{}, ErrProtocol
	}
	return limits, nil
}

func validLineSize(line []byte, max int) bool {
	return len(line) > 0 && len(line) <= max
}

func decodeStrict(line []byte, target any) error {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrProtocol
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return ErrProtocol
	}
	return nil
}

func encodeLine(message any, max int) ([]byte, error) {
	encoded, err := json.Marshal(message)
	if err != nil || len(encoded)+1 > max {
		return nil, ErrProtocol
	}
	return append(encoded, '\n'), nil
}

func encodeParentLine(message ParentMessage, max int) ([]byte, error) {
	switch message.Type {
	case TypeSubmit:
		return encodeLine(struct {
			Protocol    int               `json:"protocol"`
			Type        MessageType       `json:"type"`
			RequestID   string            `json:"request_id"`
			Text        string            `json:"text"`
			MessageID   string            `json:"message_id,omitempty"`
			Attachments []LocalAttachment `json:"attachments,omitempty"`
		}{message.Protocol, message.Type, message.RequestID, message.Text, message.MessageID, message.Attachments}, max)
	case TypeInterrupt:
		return encodeLine(struct {
			Protocol  int         `json:"protocol"`
			Type      MessageType `json:"type"`
			RequestID string      `json:"request_id"`
			MessageID string      `json:"message_id,omitempty"`
		}{message.Protocol, message.Type, message.RequestID, message.MessageID}, max)
	case TypeClose:
		return encodeLine(struct {
			Protocol int         `json:"protocol"`
			Type     MessageType `json:"type"`
		}{message.Protocol, message.Type}, max)
	case TypeReconcileAcceptedTurns:
		return encodeLine(struct {
			Protocol  int         `json:"protocol"`
			Type      MessageType `json:"type"`
			RequestID string      `json:"request_id"`
		}{message.Protocol, message.Type, message.RequestID}, max)
	case TypeInteractionResponse:
		return encodeLine(struct {
			Protocol            int                  `json:"protocol"`
			Type                MessageType          `json:"type"`
			RequestID           string               `json:"request_id"`
			InteractionResponse *InteractionResponse `json:"interaction_response"`
		}{message.Protocol, message.Type, message.RequestID, message.InteractionResponse}, max)
	default:
		return nil, ErrProtocol
	}
}

func encodeAdapterLine(message AdapterMessage, max int) ([]byte, error) {
	switch message.Type {
	case TypeReady:
		return encodeLine(struct {
			Protocol          int         `json:"protocol"`
			Type              MessageType `json:"type"`
			ProviderSessionID string      `json:"provider_session_id"`
			Readiness         string      `json:"readiness"`
			Authentication    string      `json:"authentication"`
		}{message.Protocol, message.Type, message.ProviderSessionID, message.Readiness, message.Authentication}, max)
	case TypeAccepted:
		return encodeLine(struct {
			Protocol  int         `json:"protocol"`
			Type      MessageType `json:"type"`
			RequestID string      `json:"request_id"`
			MessageID string      `json:"message_id,omitempty"`
		}{message.Protocol, message.Type, message.RequestID, message.MessageID}, max)
	case TypeEvent:
		return encodeLine(struct {
			Protocol  int         `json:"protocol"`
			Type      MessageType `json:"type"`
			RequestID string      `json:"request_id"`
			Kind      string      `json:"kind"`
			Text      string      `json:"text"`
		}{message.Protocol, message.Type, message.RequestID, message.Kind, message.Text}, max)
	case TypeFinal:
		return encodeLine(struct {
			Protocol  int         `json:"protocol"`
			Type      MessageType `json:"type"`
			RequestID string      `json:"request_id"`
			Text      string      `json:"text"`
		}{message.Protocol, message.Type, message.RequestID, message.Text}, max)
	case TypeCompleted:
		return encodeLine(struct {
			Protocol  int         `json:"protocol"`
			Type      MessageType `json:"type"`
			RequestID string      `json:"request_id"`
			Status    string      `json:"status"`
			ErrorCode string      `json:"error_code,omitempty"`
		}{message.Protocol, message.Type, message.RequestID, message.Status, message.ErrorCode}, max)
	case TypeInteractionRequest:
		return encodeLine(struct {
			Protocol           int                 `json:"protocol"`
			Type               MessageType         `json:"type"`
			RequestID          string              `json:"request_id"`
			InteractionRequest *InteractionRequest `json:"interaction_request"`
		}{message.Protocol, message.Type, message.RequestID, message.InteractionRequest}, max)
	case TypeInteractionResponseAccepted:
		return encodeLine(struct {
			Protocol          int         `json:"protocol"`
			Type              MessageType `json:"type"`
			ProviderSessionID string      `json:"provider_session_id"`
			RequestID         string      `json:"request_id"`
			MessageID         string      `json:"message_id"`
			InteractionID     string      `json:"interaction_id"`
		}{message.Protocol, message.Type, message.ProviderSessionID, message.RequestID, message.MessageID, message.InteractionID}, max)
	case TypeAcceptedTurn:
		return encodeLine(struct {
			Protocol  int         `json:"protocol"`
			Type      MessageType `json:"type"`
			RequestID string      `json:"request_id"`
			MessageID string      `json:"message_id"`
			Status    string      `json:"status"`
		}{message.Protocol, message.Type, message.RequestID, message.MessageID, message.Status}, max)
	case TypeReconciliationCompleted:
		return encodeLine(struct {
			Protocol  int         `json:"protocol"`
			Type      MessageType `json:"type"`
			RequestID string      `json:"request_id"`
		}{message.Protocol, message.Type, message.RequestID}, max)
	default:
		return nil, ErrProtocol
	}
}

type fieldRequirement struct {
	allowed  map[string]struct{}
	required map[string]struct{}
}

func fields(required []string, optional ...string) fieldRequirement {
	result := fieldRequirement{allowed: make(map[string]struct{}, len(required)+len(optional)), required: make(map[string]struct{}, len(required))}
	for _, name := range required {
		result.allowed[name] = struct{}{}
		result.required[name] = struct{}{}
	}
	for _, name := range optional {
		result.allowed[name] = struct{}{}
	}
	return result
}

func parentFields(messageType MessageType) fieldRequirement {
	switch messageType {
	case TypeSubmit:
		return fields([]string{"protocol", "type", "request_id", "text"}, "message_id", "attachments")
	case TypeInterrupt:
		return fields([]string{"protocol", "type", "request_id"})
	case TypeClose:
		return fields([]string{"protocol", "type"})
	case TypeInteractionResponse:
		return fields([]string{"protocol", "type", "request_id", "interaction_response"})
	case TypeReconcileAcceptedTurns:
		return fields([]string{"protocol", "type", "request_id"})
	default:
		return fieldRequirement{}
	}
}

func adapterFields(messageType MessageType) fieldRequirement {
	switch messageType {
	case TypeReady:
		return fields([]string{"protocol", "type", "provider_session_id", "readiness", "authentication"})
	case TypeAccepted:
		return fields([]string{"protocol", "type", "request_id"}, "message_id")
	case TypeEvent:
		return fields([]string{"protocol", "type", "request_id", "kind", "text"})
	case TypeFinal:
		return fields([]string{"protocol", "type", "request_id", "text"})
	case TypeCompleted:
		return fields([]string{"protocol", "type", "request_id", "status"}, "error_code")
	case TypeInteractionRequest:
		return fields([]string{"protocol", "type", "request_id", "interaction_request"})
	case TypeInteractionResponseAccepted:
		return fields([]string{"protocol", "type", "provider_session_id", "request_id", "message_id", "interaction_id"})
	case TypeAcceptedTurn:
		return fields([]string{"protocol", "type", "request_id", "message_id", "status"})
	case TypeReconciliationCompleted:
		return fields([]string{"protocol", "type", "request_id"})
	default:
		return fieldRequirement{}
	}
}

func validateFieldSet(line []byte, requirement fieldRequirement) error {
	if len(requirement.allowed) == 0 {
		return ErrProtocol
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	var raw map[string]json.RawMessage
	if json.Unmarshal(line, &raw) != nil {
		return ErrProtocol
	}
	for name := range raw {
		if _, ok := requirement.allowed[name]; !ok {
			return ErrProtocol
		}
	}
	for name := range requirement.required {
		if _, ok := raw[name]; !ok {
			return ErrProtocol
		}
	}
	return nil
}

func rejectDuplicateKeys(line []byte) error {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	decoder := json.NewDecoder(bytes.NewReader(line))
	if err := scanJSONValue(decoder); err != nil {
		return ErrProtocol
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return ErrProtocol
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrProtocol
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrProtocol
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return ErrProtocol
		}
		return nil
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return ErrProtocol
		}
		return nil
	default:
		return ErrProtocol
	}
}

func validateParent(message ParentMessage, limits Limits) error {
	if message.Protocol != Version {
		return ErrProtocol
	}
	switch message.Type {
	case TypeSubmit:
		if !validRequestID(message.RequestID) || !validText(message.Text, limits.MaxTextBytes) || !validOptionalOpaqueID(message.MessageID) ||
			message.InteractionResponse != nil || validateAttachments(message.Attachments, limits.MaxTextBytes) != nil {
			return ErrProtocol
		}
	case TypeInterrupt:
		if !validRequestID(message.RequestID) || message.MessageID != "" || message.Text != "" || len(message.Attachments) != 0 || message.InteractionResponse != nil {
			return ErrProtocol
		}
	case TypeClose:
		if message.RequestID != "" || message.MessageID != "" || message.Text != "" || len(message.Attachments) != 0 || message.InteractionResponse != nil {
			return ErrProtocol
		}
	case TypeInteractionResponse:
		if !validRequestID(message.RequestID) || message.MessageID != "" || message.Text != "" || len(message.Attachments) != 0 || message.InteractionResponse == nil ||
			validateInteractionResponse(*message.InteractionResponse, limits) != nil {
			return ErrProtocol
		}
	case TypeReconcileAcceptedTurns:
		if !validRequestID(message.RequestID) || message.MessageID != "" || message.Text != "" || len(message.Attachments) != 0 || message.InteractionResponse != nil {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}

func validateAttachments(attachments []LocalAttachment, maxTextBytes int) error {
	if len(attachments) > 8 {
		return ErrProtocol
	}
	for _, attachment := range attachments {
		if !filepath.IsAbs(attachment.Path) || !validText(attachment.Path, maxTextBytes) || strings.TrimSpace(attachment.Path) != attachment.Path ||
			strings.ContainsRune(attachment.Path, '\x00') || attachment.Size < 1 || attachment.Size > DefaultMaxAttachmentBytes || len(attachment.SHA256) != 64 {
			return ErrProtocol
		}
		for _, character := range attachment.Path {
			if character < 0x20 || character == 0x7f {
				return ErrProtocol
			}
		}
		for _, character := range attachment.SHA256 {
			if !strings.ContainsRune("0123456789abcdef", character) {
				return ErrProtocol
			}
		}
	}
	return nil
}

func validateAdapter(message AdapterMessage, limits Limits) error {
	if message.Protocol != Version {
		return ErrProtocol
	}
	switch message.Type {
	case TypeReady:
		if !validRequiredText(message.ProviderSessionID, limits.MaxTextBytes) || message.Readiness != "protocol" ||
			message.Authentication != "unknown" || message.RequestID != "" || message.Kind != "" || message.Text != "" ||
			message.Status != "" || message.ErrorCode != "" || message.MessageID != "" || message.InteractionRequest != nil || message.InteractionID != "" {
			return ErrProtocol
		}
	case TypeAccepted:
		if !onlyTurnCorrelation(message) || !validOptionalOpaqueID(message.MessageID) {
			return ErrProtocol
		}
	case TypeEvent:
		if !validRequestID(message.RequestID) || (message.Kind != "commentary" && message.Kind != "question") ||
			!validText(message.Text, limits.MaxTextBytes) || message.MessageID != "" || hasAdapterEnvelopeFields(message) || message.Status != "" || message.ErrorCode != "" {
			return ErrProtocol
		}
	case TypeFinal:
		if !validRequestID(message.RequestID) || message.Kind != "" || !validText(message.Text, limits.MaxTextBytes) ||
			message.MessageID != "" || hasAdapterEnvelopeFields(message) || message.Status != "" || message.ErrorCode != "" {
			return ErrProtocol
		}
	case TypeCompleted:
		if !validRequestID(message.RequestID) || message.Kind != "" || message.Text != "" || message.MessageID != "" || hasAdapterEnvelopeFields(message) ||
			!validTerminal(message.Status, message.ErrorCode) {
			return ErrProtocol
		}
	case TypeInteractionRequest:
		if !validRequestID(message.RequestID) || message.Kind != "" || message.Text != "" || message.MessageID != "" || message.Status != "" ||
			message.ErrorCode != "" || message.ProviderSessionID != "" || message.Readiness != "" || message.Authentication != "" ||
			message.InteractionRequest == nil || message.InteractionID != "" || validateInteractionRequest(*message.InteractionRequest, limits) != nil {
			return ErrProtocol
		}
	case TypeInteractionResponseAccepted:
		if !validRequiredText(message.ProviderSessionID, limits.MaxTextBytes) || !validRequestID(message.RequestID) ||
			!validOpaqueID(message.MessageID) || !validOpaqueID(message.InteractionID) || message.Kind != "" || message.Text != "" ||
			message.Status != "" || message.ErrorCode != "" || message.Readiness != "" || message.Authentication != "" || message.InteractionRequest != nil {
			return ErrProtocol
		}
	case TypeAcceptedTurn:
		if !validRequestID(message.RequestID) || !validOpaqueID(message.MessageID) ||
			(message.Status != "completed" && message.Status != "failed" && message.Status != "unknown") ||
			message.Kind != "" || message.Text != "" || message.ErrorCode != "" || hasAdapterEnvelopeFields(message) {
			return ErrProtocol
		}
	case TypeReconciliationCompleted:
		if !onlyTurnCorrelation(message) || message.MessageID != "" {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}

func onlyTurnCorrelation(message AdapterMessage) bool {
	return validRequestID(message.RequestID) && message.Kind == "" && message.Text == "" && message.Status == "" &&
		message.ErrorCode == "" && !hasAdapterEnvelopeFields(message)
}

func hasAdapterEnvelopeFields(message AdapterMessage) bool {
	return message.ProviderSessionID != "" || message.Readiness != "" || message.Authentication != "" || message.InteractionRequest != nil || message.InteractionID != ""
}

func validTerminal(status, errorCode string) bool {
	switch status {
	case "completed":
		return errorCode == ""
	case "failed":
		switch errorCode {
		case "authentication_failed", "provider_error", "protocol_error", "transport_error":
			return true
		default:
			return false
		}
	case "interrupted":
		return errorCode == "interrupted"
	default:
		return false
	}
}

func validateInteractionRequest(request InteractionRequest, limits Limits) error {
	if !validOpaqueID(request.ID) || !validOptionalOpaqueID(request.ThreadID) || !validOptionalOpaqueID(request.TurnID) ||
		!validOptionalOpaqueID(request.ItemID) || request.StartedAtMS < 0 || !validText(request.ApprovalID, limits.MaxTextBytes) ||
		!validText(request.Reason, limits.MaxTextBytes) || !validText(request.Command, limits.MaxTextBytes) ||
		!validText(request.Cwd, limits.MaxTextBytes) || !validText(request.GrantRoot, limits.MaxTextBytes) {
		return ErrProtocol
	}
	switch request.Kind {
	case InteractionQuestion:
		if len(request.Questions) == 0 || len(request.Questions) > limits.MaxQuestions || request.ApprovalID != "" ||
			request.StartedAtMS != 0 || request.Reason != "" || request.Command != "" || request.Cwd != "" ||
			request.GrantRoot != "" || request.Decisions != nil {
			return ErrProtocol
		}
		seen := make(map[string]struct{}, len(request.Questions))
		for _, question := range request.Questions {
			if !validOpaqueID(question.ID) || !validRequiredText(question.Header, limits.MaxTextBytes) ||
				!validRequiredText(question.Text, limits.MaxTextBytes) || len(question.Options) > limits.MaxOptionsPerQuestion {
				return ErrProtocol
			}
			if _, duplicate := seen[question.ID]; duplicate {
				return ErrProtocol
			}
			seen[question.ID] = struct{}{}
			optionLabels := make(map[string]struct{}, len(question.Options))
			for _, option := range question.Options {
				if !validRequiredText(option.Label, limits.MaxTextBytes) || !validText(option.Description, limits.MaxTextBytes) {
					return ErrProtocol
				}
				if _, duplicate := optionLabels[option.Label]; duplicate {
					return ErrProtocol
				}
				optionLabels[option.Label] = struct{}{}
			}
		}
	case InteractionCommandApproval:
		if len(request.Questions) != 0 || !validRequiredText(request.Command, limits.MaxTextBytes) || request.GrantRoot != "" ||
			!validDecisions(request.Decisions) {
			return ErrProtocol
		}
	case InteractionFileApproval:
		if len(request.Questions) != 0 || request.ApprovalID != "" || request.Command != "" || request.Cwd != "" ||
			!validRequiredText(request.GrantRoot, limits.MaxTextBytes) || !validDecisions(request.Decisions) {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}

func validateInteractionResponse(response InteractionResponse, limits Limits) error {
	if !validOpaqueID(response.ID) {
		return ErrProtocol
	}
	switch response.Outcome {
	case OutcomeCancelled:
		if response.Answers != nil || response.Decision != "" {
			return ErrProtocol
		}
	case OutcomeAnswered:
		if (response.Answers == nil) == (response.Decision == "") {
			return ErrProtocol
		}
		if response.Decision != "" && !validDecision(response.Decision) {
			return ErrProtocol
		}
		if response.Answers != nil {
			if len(response.Answers) == 0 || len(response.Answers) > limits.MaxQuestions {
				return ErrProtocol
			}
			for questionID, answers := range response.Answers {
				if !validOpaqueID(questionID) || len(answers) == 0 || len(answers) > limits.MaxAnswersPerQuestion {
					return ErrProtocol
				}
				for _, answer := range answers {
					if !validRequiredText(answer, limits.MaxTextBytes) {
						return ErrProtocol
					}
				}
			}
		}
	default:
		return ErrProtocol
	}
	return nil
}

func validDecisions(decisions []ApprovalDecision) bool {
	if len(decisions) == 0 || len(decisions) > 4 {
		return false
	}
	seen := make(map[ApprovalDecision]struct{}, len(decisions))
	for _, decision := range decisions {
		if !validDecision(decision) {
			return false
		}
		if _, duplicate := seen[decision]; duplicate {
			return false
		}
		seen[decision] = struct{}{}
	}
	return true
}

func validDecision(decision ApprovalDecision) bool {
	switch decision {
	case DecisionAccept, DecisionAcceptForSession, DecisionDecline, DecisionCancel:
		return true
	default:
		return false
	}
}

func containsDecision(decisions []ApprovalDecision, wanted ApprovalDecision) bool {
	for _, decision := range decisions {
		if decision == wanted {
			return true
		}
	}
	return false
}

func hasOption(options []Option, wanted string) bool {
	for _, option := range options {
		if option.Label == wanted {
			return true
		}
	}
	return false
}

func validRequestID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validOpaqueID(value string) bool {
	if !utf8.ValidString(value) || len(value) < 1 || len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validOptionalOpaqueID(value string) bool {
	return value == "" || validOpaqueID(value)
}

func validRequiredText(value string, max int) bool {
	return value != "" && strings.TrimSpace(value) != "" && validText(value, max)
}

func validText(value string, max int) bool {
	return utf8.ValidString(value) && len(value) <= max
}
