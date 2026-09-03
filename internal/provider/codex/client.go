// Package codex implements the newline-delimited JSON protocol used by the
// Codex app-server process.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const DefaultMaxMessageBytes = 1024 * 1024

var (
	ErrAlreadyInitialized            = errors.New("codex app-server client is already initialized")
	ErrActiveTurnMismatch            = errors.New("codex app-server active turn does not match interrupt request")
	ErrAcceptedHandler               = errors.New("codex app-server turn accepted handler failed")
	ErrInvalidConfiguration          = errors.New("invalid codex app-server client configuration")
	ErrInvalidRequest                = errors.New("invalid codex app-server request")
	ErrInvalidResponse               = errors.New("invalid codex app-server response")
	ErrMalformedMessage              = errors.New("malformed codex app-server message")
	ErrMessageTooLarge               = errors.New("codex app-server message exceeds configured limit")
	ErrNotInitialized                = errors.New("codex app-server client is not initialized")
	ErrNoActiveTurn                  = errors.New("codex app-server client has no active turn")
	ErrNotificationHandler           = errors.New("codex app-server notification handler failed")
	ErrServerRequestHandler          = errors.New("codex app-server request handler failed")
	ErrServerResponseAcceptedHandler = errors.New("codex app-server response acceptance handler failed")
	ErrTransport                     = errors.New("codex app-server transport failed")
	ErrUncancellableInput            = errors.New("codex app-server input cannot be closed for context cancellation")
	ErrUnexpectedEOF                 = errors.New("codex app-server stream ended before the operation completed")
	ErrUnsupportedRequest            = errors.New("unsupported request from codex app-server")
)

type ClientInfo struct {
	Name    string
	Version string
}

// Notification preserves a server notification without interpreting unknown
// methods. Params belongs to the notification and remains valid after the
// handler returns.
type Notification struct {
	Method string
	Params json.RawMessage
}

type NotificationHandler func(Notification) error

type ServerRequestKind string

const (
	ServerRequestQuestion          ServerRequestKind = "question"
	ServerRequestCommandPermission ServerRequestKind = "command_permission"
	ServerRequestFilePermission    ServerRequestKind = "file_permission"
)

type UserInputOption struct {
	Label       string
	Description string
}

type UserInputQuestion struct {
	ID       string
	Header   string
	Question string
	Options  []UserInputOption
	IsOther  bool
	IsSecret bool
}

type QuestionRequest struct {
	ItemID     string
	Questions  []UserInputQuestion
	IsBlocking bool
}

type PermissionRequest struct {
	ItemID      string
	ApprovalID  string
	StartedAtMS int64
	Reason      string
	Command     string
	Cwd         string
	GrantRoot   string
}

type ServerRequest struct {
	InteractionID string
	Kind          ServerRequestKind
	ThreadID      string
	TurnID        string
	Question      *QuestionRequest
	Permission    *PermissionRequest
}

type ApprovalDecision string

const (
	ApprovalAccept           ApprovalDecision = "accept"
	ApprovalAcceptForSession ApprovalDecision = "acceptForSession"
	ApprovalDecline          ApprovalDecision = "decline"
	ApprovalCancel           ApprovalDecision = "cancel"
)

type ServerResponse struct {
	Answers  map[string][]string
	Decision ApprovalDecision
}

type ServerRequestHandler func(context.Context, ServerRequest) (ServerResponse, error)
type ServerResponseAcceptedHandler func(ServerRequest) error

type Options struct {
	ClientInfo               ClientInfo
	MaxMessageBytes          int
	OnNotification           NotificationHandler
	OnServerRequest          ServerRequestHandler
	OnServerResponseAccepted ServerResponseAcceptedHandler
	// InputCloser is closed to unblock a read when an operation context is
	// canceled. If nil, NewClient uses input when it implements io.Closer.
	InputCloser io.Closer
}

type Sandbox struct {
	Type          string `json:"type"`
	NetworkAccess bool   `json:"networkAccess"`
}

type InitializeResult struct {
	UserAgent      string
	PlatformFamily string
	PlatformOS     string
}

type ThreadStartRequest struct {
	Cwd            string
	ApprovalPolicy string
	Sandbox        string
	// ResumeThreadID selects thread/resume instead of creating a new thread.
	// It must be the previously persisted thread.id provider binding.
	ResumeThreadID string
}

// ThreadStartResult contains the effective policy returned by app-server. The
// requested values are retained only to make policy overrides explicit.
type ThreadStartResult struct {
	ThreadID string
	// SessionID is the provider binding Bria can pass to thread/resume. Codex
	// defines that identifier as thread.id; thread.sessionId is a distinct,
	// optional live-session tree root and is exposed separately below.
	SessionID                  string
	ReportedSessionID          string
	Cwd                        string
	RequestedApprovalPolicy    string
	EffectiveApprovalPolicy    string
	HasEffectiveApprovalPolicy bool
	ApprovalPolicyOverridden   bool
	RequestedSandbox           string
	EffectiveSandbox           Sandbox
	HasEffectiveSandbox        bool
	SandboxOverridden          bool
}

// ThreadListRequest is deliberately narrower than the evolving app-server
// schema. Discovery always uses the provider state database and never asks the
// server to repair metadata by scanning rollout files.
type ThreadListRequest struct {
	Cursor string
	Limit  uint32
}

// ThreadSummary contains only metadata needed to bind and display a persisted
// Codex thread. Prompt previews and rollout paths are intentionally discarded.
type ThreadSummary struct {
	ID        string
	Cwd       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ThreadListPage struct {
	Threads    []ThreadSummary
	NextCursor string
}

type ThreadLister interface {
	ListThreads(context.Context, ThreadListRequest) (ThreadListPage, error)
}

type ThreadTurnStatus string

const (
	ThreadTurnCompleted   ThreadTurnStatus = "completed"
	ThreadTurnInterrupted ThreadTurnStatus = "interrupted"
	ThreadTurnFailed      ThreadTurnStatus = "failed"
	ThreadTurnInProgress  ThreadTurnStatus = "inProgress"
)

type ThreadTurnsListRequest struct {
	ThreadID string
	Cursor   string
	Limit    uint32
}

// ThreadTurnSummary deliberately retains correlation and disposition only.
// One turn can contain several correlated user messages after turn/steer.
type ThreadTurnSummary struct {
	ID         string
	MessageIDs []string
	Status     ThreadTurnStatus
}

type ThreadTurnsListPage struct {
	Turns      []ThreadTurnSummary
	NextCursor string
}

type ThreadTurnLister interface {
	ListThreadTurns(context.Context, ThreadTurnsListRequest) (ThreadTurnsListPage, error)
}

type TextInput struct {
	Text string
}

type LocalImageInput struct {
	Path string
}

type SandboxPolicy struct {
	Type          string
	NetworkAccess bool
}

type TurnStartRequest struct {
	ThreadID      string
	MessageID     string
	Input         []TextInput
	LocalImages   []LocalImageInput
	SandboxPolicy *SandboxPolicy
	OnAccepted    func(TurnAccepted) error
	// InterruptAfterAccepted is evaluated synchronously after OnAccepted and
	// before any buffered terminal notification can be published.
	InterruptAfterAccepted func(TurnAccepted) bool
}

type TurnAccepted struct {
	ThreadID string
	TurnID   string
}

type AgentFinal struct {
	ItemID string
	Text   string
}

// TurnOutcome is populated only from the authoritative turn/completed
// notification. Error is nil for a JSON null error and otherwise preserves the
// server's structured error value without incorporating it in a Go error.
type TurnOutcome struct {
	ThreadID              string
	TurnID                string
	Status                string
	Error                 json.RawMessage
	Final                 *AgentFinal
	InterruptAcknowledged bool
}

type RequestID int64

// RemoteError deliberately omits the server-provided message because it may
// contain local paths, prompt text, or credentials.
type RemoteError struct {
	Method string
	Code   int64
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("codex app-server method %s failed with code %d", err.Method, err.Code)
}

// TurnTerminalError reports an authoritative non-successful turn/completed
// outcome without exposing the server's free-form error payload.
type TurnTerminalError struct {
	Status         string
	HasServerError bool
}

func (err *TurnTerminalError) Error() string {
	if err.HasServerError {
		return fmt.Sprintf("codex turn completed with status %s and a server error", err.Status)
	}
	return fmt.Sprintf("codex turn completed with status %s", err.Status)
}

type Client struct {
	codec                    *codec
	clientInfo               ClientInfo
	onNotification           NotificationHandler
	onServerRequest          ServerRequestHandler
	onServerResponseAccepted ServerResponseAcceptedHandler
	inputCloser              io.Closer

	nextID      atomic.Int64
	opMu        sync.Mutex
	initialized atomic.Bool

	activeMu       sync.RWMutex
	activeThreadID string
	activeTurnID   string

	pendingMu       sync.Mutex
	pending         map[RequestID]incomingMessage
	responseWaiters map[RequestID]*responseWaiter
}

type responseWaitResult struct {
	message incomingMessage
	err     error
}

type responseWaiter struct {
	result    chan responseWaitResult
	abandoned bool
}

func NewClient(input io.Reader, output io.Writer, options Options) (*Client, error) {
	if input == nil || output == nil || options.ClientInfo.Name == "" || options.ClientInfo.Version == "" {
		return nil, ErrInvalidConfiguration
	}
	maxMessageBytes := options.MaxMessageBytes
	if maxMessageBytes == 0 {
		maxMessageBytes = DefaultMaxMessageBytes
	}
	if maxMessageBytes < 1 || maxMessageBytes == int(^uint(0)>>1) {
		return nil, ErrInvalidConfiguration
	}

	inputCloser := options.InputCloser
	if inputCloser == nil {
		inputCloser, _ = input.(io.Closer)
	}
	return &Client{
		codec:                    newCodec(input, output, maxMessageBytes),
		clientInfo:               options.ClientInfo,
		onNotification:           options.OnNotification,
		onServerRequest:          options.OnServerRequest,
		onServerResponseAccepted: options.OnServerResponseAccepted,
		inputCloser:              inputCloser,
		pending:                  make(map[RequestID]incomingMessage),
		responseWaiters:          make(map[RequestID]*responseWaiter),
	}, nil
}

func (client *Client) Initialize(ctx context.Context) (InitializeResult, error) {
	client.opMu.Lock()
	defer client.opMu.Unlock()

	if client.initialized.Load() {
		return InitializeResult{}, ErrAlreadyInitialized
	}
	params := initializeParams{
		ClientInfo: wireClientInfo{
			Name:    client.clientInfo.Name,
			Version: client.clientInfo.Version,
		},
		Capabilities: clientCapabilities{ExperimentalAPI: client.onServerRequest != nil},
	}
	response, err := client.requestAndWait(ctx, "initialize", params)
	if err != nil {
		return InitializeResult{}, err
	}
	var result initializeResult
	if err := decodeResult(response, &result); err != nil {
		return InitializeResult{}, err
	}
	if err := client.codec.writeNotification(ctx, "initialized", struct{}{}); err != nil {
		return InitializeResult{}, err
	}
	client.initialized.Store(true)
	return InitializeResult{
		UserAgent:      result.UserAgent,
		PlatformFamily: result.PlatformFamily,
		PlatformOS:     result.PlatformOS,
	}, nil
}

func (client *Client) StartThread(ctx context.Context, request ThreadStartRequest) (ThreadStartResult, error) {
	client.opMu.Lock()
	defer client.opMu.Unlock()

	if !client.initialized.Load() {
		return ThreadStartResult{}, ErrNotInitialized
	}
	if request.Cwd == "" && request.ResumeThreadID == "" {
		return ThreadStartResult{}, ErrInvalidRequest
	}
	method := "thread/start"
	var params any = threadStartParams{
		Cwd: request.Cwd, Ephemeral: false,
		ApprovalPolicy: request.ApprovalPolicy, Sandbox: request.Sandbox,
	}
	if request.ResumeThreadID != "" {
		method = "thread/resume"
		params = threadResumeParams{
			ThreadID: request.ResumeThreadID, Cwd: request.Cwd,
			ApprovalPolicy: request.ApprovalPolicy, Sandbox: request.Sandbox,
		}
	}
	response, err := client.requestAndWait(ctx, method, params)
	if err != nil {
		return ThreadStartResult{}, err
	}
	var result threadStartResult
	if err := decodeResult(response, &result); err != nil {
		return ThreadStartResult{}, err
	}
	if result.Thread.ID == "" ||
		(request.ResumeThreadID != "" && result.Thread.ID != request.ResumeThreadID) ||
		(result.Thread.Ephemeral != nil && *result.Thread.Ephemeral) {
		return ThreadStartResult{}, ErrInvalidResponse
	}
	if request.Cwd != "" && result.Thread.Cwd != nil && *result.Thread.Cwd != request.Cwd {
		return ThreadStartResult{}, ErrInvalidResponse
	}
	effectiveCwd := request.Cwd
	if result.Thread.Cwd != nil {
		effectiveCwd = *result.Thread.Cwd
	}
	hasApprovalPolicy := result.ApprovalPolicy != nil
	hasSandbox := result.Sandbox != nil
	effectiveApprovalPolicy := ""
	if hasApprovalPolicy {
		effectiveApprovalPolicy = *result.ApprovalPolicy
	}
	effectiveSandbox := Sandbox{}
	if hasSandbox {
		effectiveSandbox = *result.Sandbox
	}

	return ThreadStartResult{
		ThreadID:                   result.Thread.ID,
		SessionID:                  result.Thread.ID,
		ReportedSessionID:          result.Thread.SessionID,
		Cwd:                        effectiveCwd,
		RequestedApprovalPolicy:    request.ApprovalPolicy,
		EffectiveApprovalPolicy:    effectiveApprovalPolicy,
		HasEffectiveApprovalPolicy: hasApprovalPolicy,
		ApprovalPolicyOverridden:   request.ApprovalPolicy != "" && hasApprovalPolicy && effectiveApprovalPolicy != request.ApprovalPolicy,
		RequestedSandbox:           request.Sandbox,
		EffectiveSandbox:           effectiveSandbox,
		HasEffectiveSandbox:        hasSandbox,
		SandboxOverridden: request.Sandbox != "" && hasSandbox &&
			(normalizeSandbox(request.Sandbox) != normalizeSandbox(effectiveSandbox.Type) ||
				(normalizeSandbox(request.Sandbox) == "readonly" && effectiveSandbox.NetworkAccess)),
	}, nil
}

// ListThreads reads one official thread/list page. It does not expose provider
// prompt text or unstable on-disk paths and it forces useStateDbOnly so this
// operation cannot trigger a rollout-directory repair scan.
func (client *Client) ListThreads(ctx context.Context, request ThreadListRequest) (ThreadListPage, error) {
	client.opMu.Lock()
	defer client.opMu.Unlock()

	if !client.initialized.Load() {
		return ThreadListPage{}, ErrNotInitialized
	}
	if request.Limit == 0 || !boundedExactText(request.Cursor, 16*1024, true) {
		return ThreadListPage{}, ErrInvalidRequest
	}
	response, err := client.requestAndWait(ctx, "thread/list", threadListParams{
		Cursor: request.Cursor, Limit: request.Limit,
		SortKey: "updated_at", SortDirection: "desc", UseStateDBOnly: true,
	})
	if err != nil {
		return ThreadListPage{}, err
	}
	var result threadListResult
	if err := decodeResult(response, &result); err != nil || result.Data == nil {
		return ThreadListPage{}, ErrInvalidResponse
	}
	if len(*result.Data) > int(request.Limit) ||
		(result.NextCursor != nil && !boundedExactText(*result.NextCursor, 16*1024, false)) {
		return ThreadListPage{}, ErrInvalidResponse
	}
	page := ThreadListPage{Threads: make([]ThreadSummary, 0, len(*result.Data))}
	if result.NextCursor != nil {
		page.NextCursor = *result.NextCursor
	}
	for _, thread := range *result.Data {
		if thread.ID == nil || thread.Cwd == nil || thread.CreatedAt == nil || thread.UpdatedAt == nil || thread.Ephemeral == nil ||
			*thread.Ephemeral || *thread.CreatedAt < 0 || *thread.UpdatedAt < *thread.CreatedAt ||
			!boundedExactText(*thread.ID, 1024, false) || !boundedExactText(*thread.Cwd, 16*1024, false) {
			return ThreadListPage{}, ErrInvalidResponse
		}
		page.Threads = append(page.Threads, ThreadSummary{
			ID: *thread.ID, Cwd: *thread.Cwd,
			CreatedAt: time.Unix(*thread.CreatedAt, 0).UTC(),
			UpdatedAt: time.Unix(*thread.UpdatedAt, 0).UTC(),
		})
	}
	return page, nil
}

// ListThreadTurns reads official persisted history with full items so
// clientUserMessageId remains available as userMessage.clientId. Prompt and
// model output contents are discarded during decoding.
func (client *Client) ListThreadTurns(ctx context.Context, request ThreadTurnsListRequest) (ThreadTurnsListPage, error) {
	client.opMu.Lock()
	defer client.opMu.Unlock()

	if !client.initialized.Load() {
		return ThreadTurnsListPage{}, ErrNotInitialized
	}
	if !boundedExactText(request.ThreadID, 1024, false) || request.Limit == 0 ||
		!boundedExactText(request.Cursor, 16*1024, true) {
		return ThreadTurnsListPage{}, ErrInvalidRequest
	}
	response, err := client.requestAndWait(ctx, "thread/turns/list", threadTurnsListParams{
		ThreadID: request.ThreadID, Cursor: request.Cursor, Limit: request.Limit,
		ItemsView: "full", SortDirection: "desc",
	})
	if err != nil {
		return ThreadTurnsListPage{}, err
	}
	var result threadTurnsListResult
	if err := decodeResult(response, &result); err != nil || result.Data == nil {
		return ThreadTurnsListPage{}, ErrInvalidResponse
	}
	if len(*result.Data) > int(request.Limit) ||
		(result.NextCursor != nil && !boundedExactText(*result.NextCursor, 16*1024, false)) {
		return ThreadTurnsListPage{}, ErrInvalidResponse
	}
	page := ThreadTurnsListPage{Turns: make([]ThreadTurnSummary, 0, len(*result.Data))}
	if result.NextCursor != nil {
		page.NextCursor = *result.NextCursor
	}
	for _, turn := range *result.Data {
		if turn.ID == nil || turn.Items == nil || turn.ItemsView == nil || turn.Status == nil ||
			!boundedExactText(*turn.ID, 1024, false) || *turn.ItemsView != "full" || !validThreadTurnStatus(*turn.Status) {
			return ThreadTurnsListPage{}, ErrInvalidResponse
		}
		var messageIDs []string
		seen := make(map[string]struct{})
		for _, rawItem := range *turn.Items {
			var kind struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(rawItem, &kind); err != nil || kind.Type == "" {
				return ThreadTurnsListPage{}, ErrInvalidResponse
			}
			if kind.Type != "userMessage" {
				continue
			}
			var user struct {
				ID       *string            `json:"id"`
				ClientID *string            `json:"clientId"`
				Content  *[]json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(rawItem, &user); err != nil || user.ID == nil || user.Content == nil ||
				!boundedExactText(*user.ID, 1024, false) {
				return ThreadTurnsListPage{}, ErrInvalidResponse
			}
			if user.ClientID == nil {
				continue
			}
			if !boundedExactText(*user.ClientID, 1024, false) {
				return ThreadTurnsListPage{}, ErrInvalidResponse
			}
			if _, duplicate := seen[*user.ClientID]; duplicate {
				return ThreadTurnsListPage{}, ErrInvalidResponse
			}
			seen[*user.ClientID] = struct{}{}
			messageIDs = append(messageIDs, *user.ClientID)
		}
		page.Turns = append(page.Turns, ThreadTurnSummary{ID: *turn.ID, MessageIDs: messageIDs, Status: *turn.Status})
	}
	return page, nil
}

func validThreadTurnStatus(status ThreadTurnStatus) bool {
	switch status {
	case ThreadTurnCompleted, ThreadTurnInterrupted, ThreadTurnFailed, ThreadTurnInProgress:
		return true
	default:
		return false
	}
}

func (client *Client) StartTurn(ctx context.Context, request TurnStartRequest) (TurnOutcome, error) {
	client.opMu.Lock()
	defer client.opMu.Unlock()

	if !client.initialized.Load() {
		return TurnOutcome{}, ErrNotInitialized
	}
	if request.ThreadID == "" || len(request.Input)+len(request.LocalImages) == 0 || len(request.LocalImages) > 8 || !boundedExactText(request.MessageID, 1024, true) {
		return TurnOutcome{}, ErrInvalidRequest
	}
	input := make([]wireUserInput, 0, len(request.Input)+len(request.LocalImages))
	for _, item := range request.Input {
		if item.Text == "" {
			return TurnOutcome{}, ErrInvalidRequest
		}
		input = append(input, wireUserInput{Type: "text", Text: item.Text})
	}
	for _, image := range request.LocalImages {
		if !filepath.IsAbs(image.Path) || !boundedExactText(image.Path, 16<<10, false) {
			return TurnOutcome{}, ErrInvalidRequest
		}
		input = append(input, wireUserInput{Type: "localImage", Path: image.Path})
	}
	params := turnStartParams{ThreadID: request.ThreadID, Input: input, ClientUserMessageID: request.MessageID}
	if request.SandboxPolicy != nil {
		if request.SandboxPolicy.Type == "" {
			return TurnOutcome{}, ErrInvalidRequest
		}
		params.SandboxPolicy = &wireSandboxPolicy{
			Type:          request.SandboxPolicy.Type,
			NetworkAccess: request.SandboxPolicy.NetworkAccess,
		}
	}

	response, err := client.requestAndWait(ctx, "turn/start", params)
	if err != nil {
		return TurnOutcome{}, err
	}
	var started turnStartResult
	if err := decodeResult(response, &started); err != nil {
		return TurnOutcome{}, err
	}
	if started.Turn.ID == "" {
		return TurnOutcome{}, ErrInvalidResponse
	}
	client.setActiveTurn(request.ThreadID, started.Turn.ID)
	defer client.clearActiveTurn(request.ThreadID, started.Turn.ID)
	accepted := TurnAccepted{ThreadID: request.ThreadID, TurnID: started.Turn.ID}
	if request.OnAccepted != nil {
		if err := request.OnAccepted(accepted); err != nil {
			return TurnOutcome{}, ErrAcceptedHandler
		}
	}
	var interruptRequestID RequestID
	if request.InterruptAfterAccepted != nil && request.InterruptAfterAccepted(accepted) {
		interruptRequestID, err = client.sendRequest(ctx, "turn/interrupt", interruptParams{
			ThreadID: request.ThreadID, TurnID: started.Turn.ID,
		})
		if err != nil {
			return TurnOutcome{}, err
		}
	}

	var final *AgentFinal
	var bufferedOutcome *TurnOutcome
	var bufferedError error
	interruptAcknowledged := false
	for {
		message, err := client.readMessage(ctx)
		if err != nil {
			return TurnOutcome{}, err
		}
		if message.isNotification() {
			if err := client.emitNotification(message); err != nil {
				return TurnOutcome{}, err
			}
			switch message.Method {
			case "item/completed":
				candidate, matches, err := completedAgentFinal(message.Params, request.ThreadID, started.Turn.ID)
				if err != nil {
					return TurnOutcome{}, err
				}
				if matches {
					final = candidate
				}
			case "turn/completed":
				outcome, matches, err := completedTurn(message.Params, request.ThreadID, started.Turn.ID)
				if err != nil {
					return TurnOutcome{}, err
				}
				if matches {
					outcome, terminalError := finishTurnOutcome(outcome, final, interruptAcknowledged)
					if interruptRequestID != 0 && !interruptAcknowledged {
						bufferedOutcome = &outcome
						bufferedError = terminalError
						continue
					}
					return outcome, terminalError
				}
			}
			continue
		}
		if message.isServerRequest() {
			if client.onServerRequest != nil {
				if err := client.handleServerRequest(ctx, message, request.ThreadID, started.Turn.ID); err == nil {
					continue
				} else {
					_, _ = client.sendRequest(ctx, "turn/interrupt", interruptParams{
						ThreadID: request.ThreadID, TurnID: started.Turn.ID,
					})
					return TurnOutcome{}, err
				}
			}
			if _, err := client.sendRequest(ctx, "turn/interrupt", interruptParams{
				ThreadID: request.ThreadID, TurnID: started.Turn.ID,
			}); err != nil {
				return TurnOutcome{}, err
			}
			return TurnOutcome{}, ErrUnsupportedRequest
		}
		responseID, responseIDError := message.responseID()
		if responseIDError != nil {
			return TurnOutcome{}, responseIDError
		}
		if interruptRequestID != 0 && responseID == interruptRequestID {
			response, responseError := validateResponse("turn/interrupt", message)
			if responseError != nil {
				return TurnOutcome{}, responseError
			}
			if !isEmptyObject(response.Result) {
				return TurnOutcome{}, ErrInvalidResponse
			}
			interruptAcknowledged = true
			if bufferedOutcome != nil {
				bufferedOutcome.InterruptAcknowledged = true
				return *bufferedOutcome, bufferedError
			}
			continue
		}
		if err := client.dispatchResponse(message); err != nil {
			return TurnOutcome{}, err
		}
	}
}

func (client *Client) handleServerRequest(
	ctx context.Context,
	message incomingMessage,
	threadID string,
	turnID string,
) error {
	request, err := decodeServerRequest(message, threadID, turnID)
	if err != nil {
		return err
	}
	response, err := client.onServerRequest(ctx, request)
	if err != nil {
		return ErrServerRequestHandler
	}
	result, err := encodeServerResponse(request, response)
	if err != nil {
		return err
	}
	if err := client.codec.writeResponse(ctx, message.ID, result); err != nil {
		return err
	}
	if client.onServerResponseAccepted != nil && client.onServerResponseAccepted(request) != nil {
		return ErrServerResponseAcceptedHandler
	}
	return nil
}

func decodeServerRequest(message incomingMessage, threadID string, turnID string) (ServerRequest, error) {
	interactionID, err := normalizeServerRequestID(message.ID)
	if err != nil {
		return ServerRequest{}, err
	}
	switch message.Method {
	case "item/tool/requestUserInput":
		var params toolRequestUserInputParams
		if json.Unmarshal(message.Params, &params) != nil || params.ThreadID != threadID || params.TurnID != turnID ||
			params.ItemID == "" || params.IsBlocking == nil {
			return ServerRequest{}, ErrInvalidResponse
		}
		questions := make([]UserInputQuestion, len(params.Questions))
		seen := make(map[string]struct{}, len(params.Questions))
		for index, question := range params.Questions {
			if question.ID == "" || question.Header == "" || question.Question == "" {
				return ServerRequest{}, ErrInvalidResponse
			}
			if _, duplicate := seen[question.ID]; duplicate {
				return ServerRequest{}, ErrInvalidResponse
			}
			seen[question.ID] = struct{}{}
			options := make([]UserInputOption, len(question.Options))
			for optionIndex, option := range question.Options {
				options[optionIndex] = UserInputOption{Label: option.Label, Description: option.Description}
			}
			questions[index] = UserInputQuestion{
				ID: question.ID, Header: question.Header, Question: question.Question,
				Options: options, IsOther: question.IsOther, IsSecret: question.IsSecret,
			}
		}
		return ServerRequest{
			InteractionID: interactionID, Kind: ServerRequestQuestion,
			ThreadID: threadID, TurnID: turnID,
			Question: &QuestionRequest{ItemID: params.ItemID, Questions: questions, IsBlocking: *params.IsBlocking},
		}, nil
	case "item/commandExecution/requestApproval":
		var params commandExecutionRequestApprovalParams
		if json.Unmarshal(message.Params, &params) != nil || params.ThreadID != threadID || params.TurnID != turnID ||
			params.ItemID == "" || params.StartedAtMS == nil {
			return ServerRequest{}, ErrInvalidResponse
		}
		return ServerRequest{
			InteractionID: interactionID, Kind: ServerRequestCommandPermission,
			ThreadID: threadID, TurnID: turnID,
			Permission: &PermissionRequest{
				ItemID: params.ItemID, ApprovalID: stringValue(params.ApprovalID), StartedAtMS: *params.StartedAtMS,
				Reason: stringValue(params.Reason), Command: stringValue(params.Command), Cwd: stringValue(params.Cwd),
			},
		}, nil
	case "item/fileChange/requestApproval":
		var params fileChangeRequestApprovalParams
		if json.Unmarshal(message.Params, &params) != nil || params.ThreadID != threadID || params.TurnID != turnID ||
			params.ItemID == "" || params.StartedAtMS == nil {
			return ServerRequest{}, ErrInvalidResponse
		}
		return ServerRequest{
			InteractionID: interactionID, Kind: ServerRequestFilePermission,
			ThreadID: threadID, TurnID: turnID,
			Permission: &PermissionRequest{
				ItemID: params.ItemID, StartedAtMS: *params.StartedAtMS,
				Reason: stringValue(params.Reason), GrantRoot: stringValue(params.GrantRoot),
			},
		}, nil
	default:
		return ServerRequest{}, ErrUnsupportedRequest
	}
}

func normalizeServerRequestID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || len(raw) > 512 {
		return "", ErrInvalidResponse
	}
	var stringID string
	if json.Unmarshal(raw, &stringID) == nil {
		if stringID == "" {
			return "", ErrInvalidResponse
		}
		return "string:" + stringID, nil
	}
	var integerID int64
	if json.Unmarshal(raw, &integerID) == nil {
		return fmt.Sprintf("number:%d", integerID), nil
	}
	return "", ErrInvalidResponse
}

func encodeServerResponse(request ServerRequest, response ServerResponse) (any, error) {
	switch request.Kind {
	case ServerRequestQuestion:
		if request.Question == nil || response.Decision != "" || response.Answers == nil {
			return nil, ErrInvalidRequest
		}
		known := make(map[string]struct{}, len(request.Question.Questions))
		for _, question := range request.Question.Questions {
			known[question.ID] = struct{}{}
		}
		answers := make(map[string]toolRequestUserInputAnswer, len(response.Answers))
		for questionID, values := range response.Answers {
			if _, ok := known[questionID]; !ok || values == nil {
				return nil, ErrInvalidRequest
			}
			answers[questionID] = toolRequestUserInputAnswer{Answers: append([]string(nil), values...)}
		}
		return toolRequestUserInputResponse{Answers: answers}, nil
	case ServerRequestCommandPermission, ServerRequestFilePermission:
		if request.Permission == nil || len(response.Answers) != 0 || !validApprovalDecision(response.Decision) {
			return nil, ErrInvalidRequest
		}
		return approvalResponse{Decision: response.Decision}, nil
	default:
		return nil, ErrUnsupportedRequest
	}
}

func validApprovalDecision(decision ApprovalDecision) bool {
	switch decision {
	case ApprovalAccept, ApprovalAcceptForSession, ApprovalDecline, ApprovalCancel:
		return true
	default:
		return false
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func finishTurnOutcome(outcome TurnOutcome, final *AgentFinal, interruptAcknowledged bool) (TurnOutcome, error) {
	outcome.InterruptAcknowledged = interruptAcknowledged
	if outcome.Status != "completed" || len(outcome.Error) != 0 {
		return outcome, &TurnTerminalError{
			Status: outcome.Status, HasServerError: len(outcome.Error) != 0,
		}
	}
	outcome.Final = final
	return outcome, nil
}

// RequestInterrupt writes turn/interrupt and returns only after StartTurn's
// reader has correlated and validated the authoritative empty response.
func (client *Client) RequestInterrupt(ctx context.Context, threadID string, turnID string) (RequestID, error) {
	if !client.initialized.Load() {
		return 0, ErrNotInitialized
	}
	if threadID == "" || turnID == "" {
		return 0, ErrInvalidRequest
	}
	client.activeMu.RLock()
	if client.activeThreadID == "" || client.activeTurnID == "" {
		client.activeMu.RUnlock()
		return 0, ErrNoActiveTurn
	}
	if client.activeThreadID != threadID || client.activeTurnID != turnID {
		client.activeMu.RUnlock()
		return 0, ErrActiveTurnMismatch
	}
	id, waiter, err := client.sendRequestWithWaiter(ctx, "turn/interrupt", interruptParams{ThreadID: threadID, TurnID: turnID})
	client.activeMu.RUnlock()
	if err != nil {
		return 0, err
	}
	select {
	case result := <-waiter.result:
		if result.err != nil {
			return 0, result.err
		}
		response, err := validateResponse("turn/interrupt", result.message)
		if err != nil || !isEmptyObject(response.Result) {
			if err != nil {
				return 0, err
			}
			return 0, ErrInvalidResponse
		}
		return id, nil
	case <-ctx.Done():
		client.abandonResponseWaiter(id)
		return 0, ctx.Err()
	}
}

func (client *Client) setActiveTurn(threadID string, turnID string) {
	client.activeMu.Lock()
	defer client.activeMu.Unlock()
	client.activeThreadID = threadID
	client.activeTurnID = turnID
}

func (client *Client) clearActiveTurn(threadID string, turnID string) {
	client.activeMu.Lock()
	defer client.activeMu.Unlock()
	if client.activeThreadID == threadID && client.activeTurnID == turnID {
		client.activeThreadID = ""
		client.activeTurnID = ""
		client.cancelResponseWaiters(ErrNoActiveTurn)
	}
}

func (client *Client) requestAndWait(ctx context.Context, method string, params any) (incomingMessage, error) {
	id, err := client.sendRequest(ctx, method, params)
	if err != nil {
		return incomingMessage{}, err
	}
	if response, ok := client.takeResponse(id); ok {
		return validateResponse(method, response)
	}
	for {
		message, err := client.readMessage(ctx)
		if err != nil {
			return incomingMessage{}, err
		}
		if message.isNotification() {
			if err := client.emitNotification(message); err != nil {
				return incomingMessage{}, err
			}
			continue
		}
		if message.isServerRequest() {
			return incomingMessage{}, ErrUnsupportedRequest
		}
		responseID, err := message.responseID()
		if err != nil {
			return incomingMessage{}, err
		}
		if responseID == id {
			return validateResponse(method, message)
		}
		if err := client.storeResponse(responseID, message); err != nil {
			return incomingMessage{}, err
		}
	}
}

func (client *Client) sendRequest(ctx context.Context, method string, params any) (RequestID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	id := RequestID(client.nextID.Add(1))
	if err := client.codec.writeRequest(ctx, id, method, params); err != nil {
		return 0, err
	}
	return id, nil
}

func (client *Client) sendRequestWithWaiter(ctx context.Context, method string, params any) (RequestID, *responseWaiter, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	id := RequestID(client.nextID.Add(1))
	waiter := &responseWaiter{result: make(chan responseWaitResult, 1)}
	client.pendingMu.Lock()
	client.responseWaiters[id] = waiter
	client.pendingMu.Unlock()
	if err := client.codec.writeRequest(ctx, id, method, params); err != nil {
		client.pendingMu.Lock()
		delete(client.responseWaiters, id)
		client.pendingMu.Unlock()
		return 0, nil, err
	}
	return id, waiter, nil
}

func (client *Client) readMessage(ctx context.Context) (incomingMessage, error) {
	if err := ctx.Err(); err != nil {
		return incomingMessage{}, err
	}
	if ctx.Done() == nil {
		return client.readMessageWithoutCancellation()
	}
	if client.inputCloser == nil {
		return incomingMessage{}, ErrUncancellableInput
	}

	readFinished := make(chan struct{})
	watcherFinished := make(chan struct{})
	go func() {
		defer close(watcherFinished)
		select {
		case <-ctx.Done():
			// The close error is intentionally ignored: context cancellation is
			// authoritative and a closer may expose sensitive transport details.
			_ = client.inputCloser.Close()
		case <-readFinished:
		}
	}()
	message, err := client.codec.readMessage()
	close(readFinished)
	<-watcherFinished
	if contextError := ctx.Err(); contextError != nil {
		return incomingMessage{}, contextError
	}
	return normalizeReadResult(message, err)
}

func (client *Client) readMessageWithoutCancellation() (incomingMessage, error) {
	message, err := client.codec.readMessage()
	return normalizeReadResult(message, err)
}

func normalizeReadResult(message incomingMessage, err error) (incomingMessage, error) {
	if errors.Is(err, io.EOF) {
		return incomingMessage{}, ErrUnexpectedEOF
	}
	return message, err
}

func (client *Client) emitNotification(message incomingMessage) error {
	if client.onNotification == nil {
		return nil
	}
	notification := Notification{
		Method: message.Method,
		Params: bytes.Clone(message.Params),
	}
	if err := client.onNotification(notification); err != nil {
		return ErrNotificationHandler
	}
	return nil
}

func (client *Client) dispatchResponse(message incomingMessage) error {
	id, err := message.responseID()
	if err != nil {
		return err
	}
	client.pendingMu.Lock()
	if waiter, ok := client.responseWaiters[id]; ok {
		delete(client.responseWaiters, id)
		client.pendingMu.Unlock()
		if !waiter.abandoned {
			waiter.result <- responseWaitResult{message: message}
		}
		return nil
	}
	defer client.pendingMu.Unlock()
	if _, exists := client.pending[id]; exists {
		return ErrInvalidResponse
	}
	client.pending[id] = message
	return nil
}

func (client *Client) abandonResponseWaiter(id RequestID) {
	client.pendingMu.Lock()
	defer client.pendingMu.Unlock()
	if waiter := client.responseWaiters[id]; waiter != nil {
		waiter.abandoned = true
	}
}

func (client *Client) cancelResponseWaiters(err error) {
	client.pendingMu.Lock()
	defer client.pendingMu.Unlock()
	for id, waiter := range client.responseWaiters {
		delete(client.responseWaiters, id)
		if !waiter.abandoned {
			waiter.result <- responseWaitResult{err: err}
		}
	}
}

func (client *Client) storeResponse(id RequestID, message incomingMessage) error {
	client.pendingMu.Lock()
	defer client.pendingMu.Unlock()
	if _, exists := client.pending[id]; exists {
		return ErrInvalidResponse
	}
	client.pending[id] = message
	return nil
}

func (client *Client) takeResponse(id RequestID) (incomingMessage, bool) {
	client.pendingMu.Lock()
	defer client.pendingMu.Unlock()
	message, ok := client.pending[id]
	delete(client.pending, id)
	return message, ok
}

func validateResponse(method string, message incomingMessage) (incomingMessage, error) {
	if len(message.Error) == 0 || bytes.Equal(bytes.TrimSpace(message.Error), []byte("null")) {
		if len(message.Result) == 0 {
			return incomingMessage{}, ErrInvalidResponse
		}
		return message, nil
	}
	var remote struct {
		Code int64 `json:"code"`
	}
	if err := json.Unmarshal(message.Error, &remote); err != nil {
		return incomingMessage{}, ErrInvalidResponse
	}
	return incomingMessage{}, &RemoteError{Method: method, Code: remote.Code}
}

func isEmptyObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil && len(object) == 0
}

func decodeResult(message incomingMessage, destination any) error {
	if err := json.Unmarshal(message.Result, destination); err != nil {
		return ErrInvalidResponse
	}
	return nil
}

func completedAgentFinal(raw json.RawMessage, threadID string, turnID string) (*AgentFinal, bool, error) {
	var params itemCompletedParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, false, ErrInvalidResponse
	}
	if params.ThreadID != threadID || params.TurnID != turnID {
		return nil, false, nil
	}
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(params.Item, &kind); err != nil || kind.Type != "agentMessage" {
		return nil, false, nil
	}
	var item struct {
		ID    string `json:"id"`
		Text  string `json:"text"`
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(params.Item, &item); err != nil {
		return nil, false, ErrInvalidResponse
	}
	if item.Phase != "final_answer" {
		return nil, false, nil
	}
	if item.ID == "" {
		return nil, false, ErrInvalidResponse
	}
	return &AgentFinal{ItemID: item.ID, Text: item.Text}, true, nil
}

func completedTurn(raw json.RawMessage, threadID string, turnID string) (TurnOutcome, bool, error) {
	var params turnCompletedParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return TurnOutcome{}, false, ErrInvalidResponse
	}
	if params.ThreadID != threadID || params.Turn.ID != turnID {
		return TurnOutcome{}, false, nil
	}
	if params.Turn.Status == "" {
		return TurnOutcome{}, false, ErrInvalidResponse
	}
	var turnError json.RawMessage
	if len(params.Turn.Error) > 0 && !bytes.Equal(bytes.TrimSpace(params.Turn.Error), []byte("null")) {
		turnError = bytes.Clone(params.Turn.Error)
	}
	return TurnOutcome{
		ThreadID: threadID,
		TurnID:   turnID,
		Status:   params.Turn.Status,
		Error:    turnError,
	}, true, nil
}

func normalizeSandbox(value string) string {
	return strings.ToLower(strings.ReplaceAll(value, "-", ""))
}

type wireClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeParams struct {
	ClientInfo   wireClientInfo     `json:"clientInfo"`
	Capabilities clientCapabilities `json:"capabilities"`
}

type clientCapabilities struct {
	ExperimentalAPI bool `json:"experimentalApi,omitempty"`
}

type initializeResult struct {
	UserAgent      string `json:"userAgent"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

type threadStartParams struct {
	Cwd            string `json:"cwd"`
	Ephemeral      bool   `json:"ephemeral"`
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
}

type threadResumeParams struct {
	ThreadID       string `json:"threadId"`
	Cwd            string `json:"cwd,omitempty"`
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
}

type threadStartResult struct {
	Thread struct {
		ID        string  `json:"id"`
		SessionID string  `json:"sessionId"`
		Cwd       *string `json:"cwd"`
		Ephemeral *bool   `json:"ephemeral"`
	} `json:"thread"`
	ApprovalPolicy *string  `json:"approvalPolicy"`
	Sandbox        *Sandbox `json:"sandbox"`
}

type threadListParams struct {
	Cursor         string `json:"cursor,omitempty"`
	Limit          uint32 `json:"limit"`
	SortKey        string `json:"sortKey"`
	SortDirection  string `json:"sortDirection"`
	UseStateDBOnly bool   `json:"useStateDbOnly"`
}

type threadListResult struct {
	Data       *[]wireThreadSummary `json:"data"`
	NextCursor *string              `json:"nextCursor"`
}

type threadTurnsListParams struct {
	ThreadID      string `json:"threadId"`
	Cursor        string `json:"cursor,omitempty"`
	Limit         uint32 `json:"limit"`
	ItemsView     string `json:"itemsView"`
	SortDirection string `json:"sortDirection"`
}

type threadTurnsListResult struct {
	Data       *[]wireThreadTurn `json:"data"`
	NextCursor *string           `json:"nextCursor"`
}

type wireThreadTurn struct {
	ID        *string            `json:"id"`
	Items     *[]json.RawMessage `json:"items"`
	ItemsView *string            `json:"itemsView"`
	Status    *ThreadTurnStatus  `json:"status"`
}

type wireThreadSummary struct {
	ID        *string `json:"id"`
	Cwd       *string `json:"cwd"`
	CreatedAt *int64  `json:"createdAt"`
	UpdatedAt *int64  `json:"updatedAt"`
	Ephemeral *bool   `json:"ephemeral"`
}

func boundedExactText(value string, maxBytes int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

type wireUserInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
}

type wireSandboxPolicy struct {
	Type          string `json:"type"`
	NetworkAccess bool   `json:"networkAccess"`
}

type turnStartParams struct {
	ThreadID            string             `json:"threadId"`
	Input               []wireUserInput    `json:"input"`
	ClientUserMessageID string             `json:"clientUserMessageId,omitempty"`
	SandboxPolicy       *wireSandboxPolicy `json:"sandboxPolicy,omitempty"`
}

type turnStartResult struct {
	Turn struct {
		ID string `json:"id"`
	} `json:"turn"`
}

type interruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type toolRequestUserInputParams struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	ItemID     string `json:"itemId"`
	IsBlocking *bool  `json:"isBlocking"`
	Questions  []struct {
		ID       string `json:"id"`
		Header   string `json:"header"`
		Question string `json:"question"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
		IsOther  bool `json:"isOther"`
		IsSecret bool `json:"isSecret"`
	} `json:"questions"`
}

type commandExecutionRequestApprovalParams struct {
	ThreadID    string  `json:"threadId"`
	TurnID      string  `json:"turnId"`
	ItemID      string  `json:"itemId"`
	ApprovalID  *string `json:"approvalId"`
	StartedAtMS *int64  `json:"startedAtMs"`
	Reason      *string `json:"reason"`
	Command     *string `json:"command"`
	Cwd         *string `json:"cwd"`
}

type fileChangeRequestApprovalParams struct {
	ThreadID    string  `json:"threadId"`
	TurnID      string  `json:"turnId"`
	ItemID      string  `json:"itemId"`
	StartedAtMS *int64  `json:"startedAtMs"`
	Reason      *string `json:"reason"`
	GrantRoot   *string `json:"grantRoot"`
}

type toolRequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}

type toolRequestUserInputResponse struct {
	Answers map[string]toolRequestUserInputAnswer `json:"answers"`
}

type approvalResponse struct {
	Decision ApprovalDecision `json:"decision"`
}

type itemCompletedParams struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	Item     json.RawMessage `json:"item"`
}

type turnCompletedParams struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID     string          `json:"id"`
		Status string          `json:"status"`
		Error  json.RawMessage `json:"error"`
	} `json:"turn"`
}
