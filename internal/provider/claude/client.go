// Package claude implements the local Claude Code stream-json protocol.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const DefaultMaxEventBytes = 1 << 20

var (
	ErrSessionMismatch         = errors.New("claude event session does not match initialized session")
	ErrEventOutOfOrder         = errors.New("claude event is out of order")
	ErrEventTooLarge           = errors.New("claude event exceeds the configured limit")
	ErrMalformedEvent          = errors.New("malformed claude event")
	ErrTurnInProgress          = errors.New("a claude turn is already in progress")
	ErrReplayMismatch          = errors.New("claude replay does not match the pending input")
	ErrTransport               = errors.New("claude stream transport failed")
	ErrCancellationUnsupported = errors.New("claude reader cannot be interrupted")
	ErrClientClosed            = errors.New("claude stream client is closed")
)

type Options struct {
	MaxEventBytes int
}

type EventKind uint8

const (
	EventUnknown EventKind = iota
	EventInit
	EventUserReplay
	EventInternalUser
	EventAssistant
	EventPermissionRequest
	EventResult
)

type PermissionRequest struct {
	RequestID   string
	ToolName    string
	ToolUseID   string
	Command     string
	CWD         string
	Path        string
	Title       string
	Description string
}

type PermissionDecision uint8

const (
	PermissionAllow PermissionDecision = iota + 1
	PermissionDeny
	PermissionDenyAndInterrupt
)

type FailureCode string

const (
	FailureNone           FailureCode = ""
	FailureAuthentication FailureCode = "authentication_failed"
	FailureProvider       FailureCode = "provider_error"
)

type InitEvent struct {
	CWD               string
	ClaudeCodeVersion string
	Model             string
	PermissionMode    string
}

type UserReplayEvent struct {
	Text      string
	MessageID string
}

type AssistantEvent struct {
	Text        string
	Tools       []string
	FailureCode FailureCode
}

type ResultEvent struct {
	IsError        bool
	TerminalReason string
	FailureCode    FailureCode
	Text           string
}

// TerminalSuccess combines the terminal protocol result with the separately
// observed process outcome. Claude 2.1.181 can emit subtype "success" while
// is_error is true, so subtype is deliberately not part of this decision.
func (result ResultEvent) TerminalSuccess(processErr error) bool {
	return !result.IsError && result.FailureCode == FailureNone && processErr == nil
}

type Event struct {
	Kind        EventKind
	SessionID   string
	UnknownType string
	Init        *InitEvent
	UserReplay  *UserReplayEvent
	Assistant   *AssistantEvent
	Permission  *PermissionRequest
	Result      *ResultEvent
}

type phase uint8

const (
	phaseInit phase = iota
	phaseReplay
	phaseTurn
)

// Client exchanges newline-delimited stream-json with one already-started
// Claude process. It never starts or closes the process or its streams. A
// terminal result ends one turn only; the caller may send another turn and
// closes the input when the long-lived process is no longer needed.
type Client struct {
	reader        *bufio.Reader
	writer        io.Writer
	readerCloser  io.Closer
	writerCloser  io.Closer
	maxEventBytes int

	readMu   sync.Mutex
	writeMu  sync.Mutex
	stateMu  sync.Mutex
	close    sync.Once
	closeErr error

	phase              phase
	initialized        bool
	sessionID          string
	turnPending        bool
	pendingHash        [sha256.Size]byte
	pendingMessageID   string
	turnFailure        FailureCode
	closed             bool
	pendingPermissions map[string]struct{}
}

func NewClient(reader io.Reader, writer io.Writer, options Options) (*Client, error) {
	if reader == nil || writer == nil {
		return nil, errors.New("claude reader and writer are required")
	}
	maxEventBytes := options.MaxEventBytes
	if maxEventBytes == 0 {
		maxEventBytes = DefaultMaxEventBytes
	}
	if maxEventBytes < 1 {
		return nil, errors.New("maximum claude event size must be positive")
	}
	client := &Client{
		reader:             bufio.NewReader(reader),
		writer:             writer,
		maxEventBytes:      maxEventBytes,
		phase:              phaseInit,
		pendingPermissions: make(map[string]struct{}),
	}
	client.readerCloser, _ = reader.(io.Closer)
	client.writerCloser, _ = writer.(io.Closer)
	return client, nil
}

// RespondPermission writes one exact SDK control_response for a previously
// observed can_use_tool request. Free-form provider text is never echoed.
func (client *Client) RespondPermission(ctx context.Context, requestID string, decision PermissionDecision) error {
	if ctx == nil || ctx.Err() != nil || !validControlID(requestID) ||
		(decision != PermissionAllow && decision != PermissionDeny && decision != PermissionDenyAndInterrupt) {
		return ErrMalformedEvent
	}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	client.stateMu.Lock()
	if client.closed {
		client.stateMu.Unlock()
		return ErrClientClosed
	}
	if _, pending := client.pendingPermissions[requestID]; !pending {
		client.stateMu.Unlock()
		return ErrMalformedEvent
	}
	delete(client.pendingPermissions, requestID)
	client.stateMu.Unlock()

	behavior := permissionBehavior{Behavior: "allow"}
	if decision != PermissionAllow {
		behavior.Behavior = "deny"
		behavior.Message = "Denied by user"
		behavior.Interrupt = decision == PermissionDenyAndInterrupt
	}
	envelope := controlResponseEnvelope{Type: "control_response"}
	envelope.Response.Subtype = "success"
	envelope.Response.RequestID = requestID
	envelope.Response.Response = behavior
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return ErrMalformedEvent
	}
	encoded = append(encoded, '\n')
	if err := writeAll(client.writer, encoded); err != nil {
		_ = client.Close()
		return ErrTransport
	}
	return nil
}

// SendUser writes the exact text envelope accepted by Claude Code 2.1.181.
// Only one turn may be outstanding; receipt and completion are established by
// the replay and result events, not by a successful write alone.
func (client *Client) SendUser(text string) error {
	return client.sendUser(text, "")
}

// SendUserWithID carries the durable caller identity in Claude Code's
// stream-json uuid field. Claude persists and deduplicates that field in the
// exact provider session; Bria does not infer identity from prompt text.
func (client *Client) SendUserWithID(text, messageID string) error {
	if !validUserMessageID(messageID) {
		return ErrMalformedEvent
	}
	return client.sendUser(text, messageID)
}

func (client *Client) sendUser(text, messageID string) error {
	if !utf8.ValidString(text) {
		return ErrMalformedEvent
	}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()

	client.stateMu.Lock()
	if client.closed {
		client.stateMu.Unlock()
		return ErrClientClosed
	}
	if client.turnPending {
		client.stateMu.Unlock()
		return ErrTurnInProgress
	}
	pendingHash := sha256.Sum256([]byte(text))
	client.turnPending = true
	client.pendingHash = pendingHash
	client.pendingMessageID = messageID
	client.turnFailure = FailureNone
	client.stateMu.Unlock()

	envelope := userInputEnvelope{
		Type: "user", UUID: messageID,
		Message: messageInput{
			Role: "user",
			Content: []contentInput{{
				Type: "text",
				Text: text,
			}},
		},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		client.rollbackPendingTurn(pendingHash)
		return fmt.Errorf("encode claude user input: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeAll(client.writer, encoded); err != nil {
		client.rollbackPendingTurn(pendingHash)
		_ = client.Close()
		return ErrTransport
	}

	client.stateMu.Lock()
	closed := client.closed
	client.stateMu.Unlock()
	if closed {
		return ErrClientClosed
	}
	return nil
}

func (client *Client) rollbackPendingTurn(expectedHash [sha256.Size]byte) {
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	if client.turnPending && client.pendingHash == expectedHash {
		client.turnPending = false
		client.pendingHash = [sha256.Size]byte{}
		client.pendingMessageID = ""
		client.turnFailure = FailureNone
	}
}

// NextEvent reads, validates, and orders one event. Unknown event types are
// returned without exposing their raw payload. Known malformed events fail
// closed and errors never include the source line or provider message text.
func (client *Client) NextEvent() (Event, error) {
	client.readMu.Lock()
	defer client.readMu.Unlock()

	line, err := client.readLine()
	if err != nil {
		return Event{}, err
	}
	if !utf8.Valid(line) {
		return Event{}, ErrMalformedEvent
	}
	event, _, err := decodeEvent(line)
	if err != nil {
		return Event{}, err
	}

	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	if client.initialized && event.SessionID != "" && event.SessionID != client.sessionID {
		return Event{}, ErrSessionMismatch
	}
	if event.Kind == EventUnknown {
		return event, nil
	}
	switch event.Kind {
	case EventInit:
		if client.phase != phaseInit || !client.turnPending {
			return Event{}, ErrEventOutOfOrder
		}
		client.initialized = true
		client.sessionID = event.SessionID
		client.phase = phaseReplay
	case EventUserReplay:
		if client.phase != phaseReplay || !client.turnPending {
			return Event{}, ErrEventOutOfOrder
		}
		if sha256.Sum256([]byte(event.UserReplay.Text)) != client.pendingHash {
			return Event{}, ErrReplayMismatch
		}
		if client.pendingMessageID != "" && event.UserReplay.MessageID != client.pendingMessageID {
			return Event{}, ErrReplayMismatch
		}
		client.phase = phaseTurn
	case EventInternalUser:
		if client.phase != phaseTurn || !client.turnPending {
			return Event{}, ErrEventOutOfOrder
		}
	case EventAssistant:
		if client.phase != phaseTurn || !client.turnPending {
			return Event{}, ErrEventOutOfOrder
		}
		if event.Assistant.FailureCode != FailureNone {
			client.turnFailure = event.Assistant.FailureCode
		}
	case EventPermissionRequest:
		if client.phase != phaseTurn || !client.turnPending || event.Permission == nil {
			return Event{}, ErrEventOutOfOrder
		}
		if _, duplicate := client.pendingPermissions[event.Permission.RequestID]; duplicate {
			return Event{}, ErrMalformedEvent
		}
		client.pendingPermissions[event.Permission.RequestID] = struct{}{}
	case EventResult:
		if client.phase != phaseTurn || !client.turnPending {
			return Event{}, ErrEventOutOfOrder
		}
		event.Result.FailureCode = client.turnFailure
		client.turnFailure = FailureNone
		client.turnPending = false
		client.pendingHash = [sha256.Size]byte{}
		client.pendingMessageID = ""
		client.phase = phaseReplay
	default:
		return Event{}, ErrMalformedEvent
	}
	return event, nil
}

// NextEventContext interrupts a silent process pipe by closing this client when
// the context ends. Process stdout pipes implement io.Closer; callers with an
// arbitrary non-closable reader get an explicit error before any blocking read.
func (client *Client) NextEventContext(ctx context.Context) (Event, error) {
	if ctx == nil {
		return Event{}, errors.New("claude event context is required")
	}
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if client.readerCloser == nil {
		return Event{}, ErrCancellationUnsupported
	}

	readDone := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-readDone:
		}
	}()

	event, err := client.NextEvent()
	close(readDone)
	<-watcherDone
	if contextErr := ctx.Err(); contextErr != nil {
		return Event{}, contextErr
	}
	return event, err
}

// Close makes future writes fail and closes any caller-supplied closable
// streams. Underlying close details are deliberately not returned.
func (client *Client) Close() error {
	client.close.Do(func() {
		client.stateMu.Lock()
		client.closed = true
		client.stateMu.Unlock()

		if client.readerCloser != nil {
			if err := client.readerCloser.Close(); err != nil {
				client.closeErr = ErrTransport
			}
		}
		if client.writerCloser != nil {
			if err := client.writerCloser.Close(); err != nil {
				client.closeErr = ErrTransport
			}
		}
	})
	return client.closeErr
}

func (client *Client) ProtocolInitialized() bool {
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	return client.initialized
}

func (client *Client) SessionID() (string, bool) {
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	return client.sessionID, client.initialized
}

func (client *Client) readLine() ([]byte, error) {
	line := make([]byte, 0, min(client.maxEventBytes, 4096))
	for {
		fragment, err := client.reader.ReadSlice('\n')
		candidate := append(line, fragment...)
		endsInNewline := len(candidate) > 0 && candidate[len(candidate)-1] == '\n'
		contentLength := len(candidate)
		if endsInNewline {
			contentLength--
			if contentLength > 0 && candidate[contentLength-1] == '\r' {
				contentLength--
			}
		}
		if contentLength > client.maxEventBytes {
			return nil, ErrEventTooLarge
		}
		line = candidate

		switch {
		case err == nil:
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				return nil, ErrMalformedEvent
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return line, nil
		case errors.Is(err, io.EOF):
			return nil, io.EOF
		default:
			return nil, ErrTransport
		}
	}
}

func decodeEvent(line []byte) (Event, phase, error) {
	var envelope eventEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil || strings.TrimSpace(envelope.Type) == "" {
		return Event{}, phaseInit, ErrMalformedEvent
	}

	switch envelope.Type {
	case "system":
		if envelope.Subtype != "init" {
			return Event{Kind: EventUnknown, SessionID: envelope.SessionID, UnknownType: envelope.Type}, phaseInit, nil
		}
		var raw initWireEvent
		if err := json.Unmarshal(line, &raw); err != nil ||
			blank(raw.SessionID) || blank(raw.CWD) || blank(raw.ClaudeCodeVersion) {
			return Event{}, phaseInit, ErrMalformedEvent
		}
		return Event{
			Kind:      EventInit,
			SessionID: raw.SessionID,
			Init: &InitEvent{
				CWD:               raw.CWD,
				ClaudeCodeVersion: raw.ClaudeCodeVersion,
				Model:             raw.Model,
				PermissionMode:    raw.PermissionMode,
			},
		}, phaseInit, nil
	case "user":
		var raw userWireEvent
		if err := json.Unmarshal(line, &raw); err != nil || blank(raw.SessionID) ||
			raw.Message == nil ||
			raw.Message.Role != "user" || raw.Message.Content == nil {
			return Event{}, phaseReplay, ErrMalformedEvent
		}
		if raw.IsReplay != nil && *raw.IsReplay {
			text, valid := replayText(*raw.Message.Content)
			if !valid || (raw.UUID != "" && !validUserMessageID(raw.UUID)) {
				return Event{}, phaseReplay, ErrMalformedEvent
			}
			return Event{
				Kind:       EventUserReplay,
				SessionID:  raw.SessionID,
				UserReplay: &UserReplayEvent{Text: text, MessageID: raw.UUID},
			}, phaseReplay, nil
		}
		if !validContentBlocks(*raw.Message.Content) {
			return Event{}, phaseTurn, ErrMalformedEvent
		}
		return Event{
			Kind:      EventInternalUser,
			SessionID: raw.SessionID,
		}, phaseTurn, nil
	case "assistant":
		var raw assistantWireEvent
		if err := json.Unmarshal(line, &raw); err != nil || blank(raw.SessionID) ||
			raw.Message == nil || raw.Message.Role != "assistant" || raw.Message.Content == nil {
			return Event{}, phaseTurn, ErrMalformedEvent
		}
		failure := safeFailureCode(raw.Error)
		text, tools, valid := assistantContent(*raw.Message.Content)
		if !valid {
			return Event{}, phaseTurn, ErrMalformedEvent
		}
		if failure != FailureNone {
			text = ""
		}
		return Event{
			Kind:      EventAssistant,
			SessionID: raw.SessionID,
			Assistant: &AssistantEvent{Text: text, Tools: tools, FailureCode: failure},
		}, phaseTurn, nil
	case "control_request":
		var raw controlRequestWire
		if err := json.Unmarshal(line, &raw); err != nil || raw.Type != "control_request" || !validControlID(raw.RequestID) ||
			raw.Request.Subtype != "can_use_tool" || blank(raw.Request.ToolName) || blank(raw.Request.ToolUseID) || raw.Request.Input == nil {
			return Event{}, phaseTurn, ErrMalformedEvent
		}
		permission := &PermissionRequest{
			RequestID: raw.RequestID, ToolName: raw.Request.ToolName, ToolUseID: raw.Request.ToolUseID,
			Command: selectedString(raw.Request.Input, "command"), CWD: selectedString(raw.Request.Input, "cwd"),
			Path:  firstSelectedString(raw.Request.Input, "file_path", "path", "notebook_path"),
			Title: raw.Request.Title, Description: firstNonBlank(raw.Request.Description, selectedString(raw.Request.Input, "description")),
		}
		for _, value := range []string{permission.ToolName, permission.ToolUseID, permission.Command, permission.CWD, permission.Path, permission.Title, permission.Description} {
			if !utf8.ValidString(value) || len(value) > clientSafeTextLimit {
				return Event{}, phaseTurn, ErrMalformedEvent
			}
		}
		return Event{Kind: EventPermissionRequest, Permission: permission}, phaseTurn, nil
	case "result":
		var raw resultWireEvent
		if err := json.Unmarshal(line, &raw); err != nil || blank(raw.SessionID) ||
			raw.IsError == nil || blank(raw.TerminalReason) {
			return Event{}, phaseTurn, ErrMalformedEvent
		}
		if !*raw.IsError && raw.Result == nil {
			return Event{}, phaseTurn, ErrMalformedEvent
		}
		text := ""
		if !*raw.IsError {
			text = *raw.Result
		}
		return Event{
			Kind:      EventResult,
			SessionID: raw.SessionID,
			Result: &ResultEvent{
				IsError:        *raw.IsError,
				TerminalReason: raw.TerminalReason,
				Text:           text,
			},
		}, phaseTurn, nil
	default:
		return Event{
			Kind:        EventUnknown,
			SessionID:   envelope.SessionID,
			UnknownType: envelope.Type,
		}, phaseInit, nil
	}
}

func replayText(content []contentWire) (string, bool) {
	if len(content) != 1 || content[0].Type != "text" {
		return "", false
	}
	var text *string
	if len(content[0].Text) == 0 || json.Unmarshal(content[0].Text, &text) != nil || text == nil {
		return "", false
	}
	return *text, true
}

func validContentBlocks(content []contentWire) bool {
	if len(content) == 0 {
		return false
	}
	for _, block := range content {
		if blank(block.Type) {
			return false
		}
	}
	return true
}

func assistantContent(content []contentWire) (text string, tools []string, valid bool) {
	var texts []string
	for _, block := range content {
		if blank(block.Type) {
			return "", nil, false
		}
		switch block.Type {
		case "text":
			var text *string
			if len(block.Text) == 0 || json.Unmarshal(block.Text, &text) != nil || text == nil {
				return "", nil, false
			}
			texts = append(texts, *text)
		case "tool_use":
			if !blank(block.Name) {
				tools = append(tools, block.Name)
			}
		}
	}
	return strings.Join(texts, ""), tools, true
}

func safeFailureCode(code string) FailureCode {
	switch code {
	case "":
		return FailureNone
	case string(FailureAuthentication):
		return FailureAuthentication
	default:
		return FailureProvider
	}
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }

func writeAll(writer io.Writer, document []byte) error {
	for len(document) > 0 {
		written, err := writer.Write(document)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		if written < 0 || written > len(document) {
			return io.ErrShortWrite
		}
		document = document[written:]
	}
	return nil
}

type userInputEnvelope struct {
	Type    string       `json:"type"`
	UUID    string       `json:"uuid,omitempty"`
	Message messageInput `json:"message"`
}

type messageInput struct {
	Role    string         `json:"role"`
	Content []contentInput `json:"content"`
}

type contentInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type eventEnvelope struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
}

type initWireEvent struct {
	SessionID         string `json:"session_id"`
	CWD               string `json:"cwd"`
	ClaudeCodeVersion string `json:"claude_code_version"`
	Model             string `json:"model"`
	PermissionMode    string `json:"permissionMode"`
}

type contentWire struct {
	Type string          `json:"type"`
	Text json.RawMessage `json:"text"`
	Name string          `json:"name"`
}

type messageWire struct {
	Role    string         `json:"role"`
	Content *[]contentWire `json:"content"`
}

type userWireEvent struct {
	SessionID string       `json:"session_id"`
	UUID      string       `json:"uuid"`
	IsReplay  *bool        `json:"isReplay"`
	Message   *messageWire `json:"message"`
}

type assistantWireEvent struct {
	SessionID string       `json:"session_id"`
	Error     string       `json:"error"`
	Message   *messageWire `json:"message"`
}

type resultWireEvent struct {
	SessionID      string  `json:"session_id"`
	IsError        *bool   `json:"is_error"`
	TerminalReason string  `json:"terminal_reason"`
	Result         *string `json:"result"`
}

const clientSafeTextLimit = 32 << 10

type controlRequestWire struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype     string                     `json:"subtype"`
		ToolName    string                     `json:"tool_name"`
		Input       map[string]json.RawMessage `json:"input"`
		ToolUseID   string                     `json:"tool_use_id"`
		Title       string                     `json:"title"`
		Description string                     `json:"description"`
	} `json:"request"`
}

type permissionBehavior struct {
	Behavior  string `json:"behavior"`
	Message   string `json:"message,omitempty"`
	Interrupt bool   `json:"interrupt,omitempty"`
}

type controlResponseEnvelope struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string             `json:"subtype"`
		RequestID string             `json:"request_id"`
		Response  permissionBehavior `json:"response"`
	} `json:"response"`
}

func selectedString(input map[string]json.RawMessage, key string) string {
	var value string
	if json.Unmarshal(input[key], &value) != nil {
		return ""
	}
	return value
}

func firstSelectedString(input map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if value := selectedString(input, key); value != "" {
			return value
		}
	}
	return ""
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if !blank(value) {
			return value
		}
	}
	return ""
}

func validControlID(value string) bool {
	if len(value) < 1 || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validUserMessageID(value string) bool {
	if len(value) < 1 || len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
