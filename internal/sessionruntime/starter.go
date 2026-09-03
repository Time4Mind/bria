// Package sessionruntime owns provider-neutral adapter processes and their
// bounded, versioned JSONL control protocol.
package sessionruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/processgroup"
	"bria/internal/runtimeprotocol"
)

const (
	defaultHandshakeTimeout = 5 * time.Second
	defaultCloseTimeout     = 2 * time.Second
	defaultTerminateTimeout = 2 * time.Second
	defaultMaxLineBytes     = 64 * 1024
	defaultMaxTextBytes     = 32 * 1024
	defaultMaxTurnEvents    = 256
)

var (
	ErrSessionAlreadyTracked = errors.New("session process is already tracked")
	ErrSessionNotTracked     = errors.New("session process is not tracked")
	ErrBindingMismatch       = errors.New("provider binding does not match tracked process")
	ErrNoTurnInFlight        = errors.New("session has no turn in flight")
	ErrInterruptUnconfirmed  = errors.New("provider did not confirm turn interruption")
)

var (
	_ app.SessionStarter   = (*Starter)(nil)
	_ Submitter            = (*Starter)(nil)
	_ InteractiveSubmitter = (*Starter)(nil)
	_ TurnStopper          = (*Starter)(nil)
	_ ProcessSupervisor    = (*Starter)(nil)
)

// CommandSpec configures one provider-neutral adapter executable. Path is
// executed directly; Args are never interpreted by a shell.
type CommandSpec struct {
	Path                   string
	Args                   []string
	Env                    []string
	ProviderCredentialFile string
}

// Options bounds process readiness, shutdown, and every protocol payload.
// MaxHandshakeBytes is a compatibility alias for MaxLineBytes.
type Options struct {
	HandshakeTimeout         time.Duration
	GracefulCloseTimeout     time.Duration
	GracefulTerminateTimeout time.Duration
	MaxHandshakeBytes        int
	MaxLineBytes             int
	MaxTextBytes             int
	MaxTurnEvents            int
	MaxReconciledTurns       int
}

type Starter struct {
	commands           map[domain.Provider]verifiedCommand
	handshakeTimeout   time.Duration
	closeTimeout       time.Duration
	terminateTimeout   time.Duration
	maxLineBytes       int
	maxTextBytes       int
	maxTurnEvents      int
	maxReconciledTurns int
	requestSequence    atomic.Uint64

	mu         sync.Mutex
	processes  map[domain.SessionID]*processRecord
	tombstones map[domain.SessionID]processTombstone
}

type verifiedCommand struct {
	spec     CommandSpec
	identity os.FileInfo
}

type processTombstone struct {
	request app.StartSessionRequest
	binding domain.ProviderBinding
}

type processRecord struct {
	request    app.StartSessionRequest
	command    *exec.Cmd
	stdin      io.WriteCloser
	output     chan wireResult
	outputEOF  chan struct{}
	readerStop chan struct{}
	done       chan struct{}

	writeMu        sync.Mutex
	turnMu         sync.Mutex
	turn           *activeTurn
	lifecycleMu    sync.Mutex
	reaping        bool
	readerStopOnce sync.Once

	generation uint64
	binding    domain.ProviderBinding
}

type activeTurn struct {
	requestID          string
	done               chan struct{}
	interruptSent      bool
	interruptConfirmed bool
	terminalErr        error
}

type wireMessage struct {
	Protocol           int
	Type               string
	ProviderSessionID  string
	Readiness          string
	Authentication     AuthenticationState
	RequestID          string
	MessageID          string
	Kind               EventKind
	Text               string
	Status             string
	ErrorCode          string
	InteractionRequest *runtimeprotocol.InteractionRequest
	InteractionID      string
}

type wireResult struct {
	message wireMessage
	err     error
}

type submitEnvelope struct {
	Protocol    int               `json:"protocol"`
	Type        string            `json:"type"`
	RequestID   string            `json:"request_id"`
	Text        string            `json:"text"`
	MessageID   string            `json:"message_id,omitempty"`
	Attachments []LocalAttachment `json:"attachments,omitempty"`
}

type requestEnvelope struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

type closeEnvelope struct {
	Protocol int    `json:"protocol"`
	Type     string `json:"type"`
}

func NewStarter(commands map[domain.Provider]CommandSpec, options Options) (*Starter, error) {
	if len(commands) == 0 {
		return nil, errors.New("at least one provider command is required")
	}
	configured := make(map[domain.Provider]verifiedCommand, len(commands))
	for provider, command := range commands {
		if provider != domain.ProviderCodex && provider != domain.ProviderClaude {
			return nil, fmt.Errorf("unsupported provider %q", provider)
		}
		verified, err := verifyCommand(command)
		if err != nil {
			return nil, fmt.Errorf("provider %q command: %w", provider, err)
		}
		configured[provider] = verified
	}

	handshakeTimeout := options.HandshakeTimeout
	if handshakeTimeout == 0 {
		handshakeTimeout = defaultHandshakeTimeout
	}
	closeTimeout := options.GracefulCloseTimeout
	if closeTimeout == 0 {
		closeTimeout = defaultCloseTimeout
	}
	terminateTimeout := options.GracefulTerminateTimeout
	if terminateTimeout == 0 {
		terminateTimeout = defaultTerminateTimeout
	}
	maxLineBytes := options.MaxLineBytes
	if maxLineBytes == 0 {
		maxLineBytes = options.MaxHandshakeBytes
	}
	if maxLineBytes == 0 {
		maxLineBytes = defaultMaxLineBytes
	}
	maxTextBytes := options.MaxTextBytes
	if maxTextBytes == 0 {
		maxTextBytes = defaultMaxTextBytes
	}
	maxTurnEvents := options.MaxTurnEvents
	if maxTurnEvents == 0 {
		maxTurnEvents = defaultMaxTurnEvents
	}
	maxReconciledTurns := options.MaxReconciledTurns
	if maxReconciledTurns == 0 {
		maxReconciledTurns = 10_000
	}
	if handshakeTimeout < 0 || closeTimeout < 0 || terminateTimeout < 0 {
		return nil, errors.New("process timeouts must be positive")
	}
	if maxLineBytes < 128 || maxTextBytes < 1 || maxTurnEvents < 1 || maxReconciledTurns < 1 || maxReconciledTurns > 100_000 {
		return nil, errors.New("protocol limits are too small")
	}

	return &Starter{
		commands: configured, handshakeTimeout: handshakeTimeout,
		closeTimeout: closeTimeout, terminateTimeout: terminateTimeout,
		maxLineBytes: maxLineBytes,
		maxTextBytes: maxTextBytes, maxTurnEvents: maxTurnEvents, maxReconciledTurns: maxReconciledTurns,
		processes:  make(map[domain.SessionID]*processRecord),
		tombstones: make(map[domain.SessionID]processTombstone),
	}, nil
}

// Start directly executes one adapter in the exact requested directory and
// binds only after protocol-level readiness. This never implies authentication.
func (starter *Starter) Start(ctx context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	if err := validateRequest(request); err != nil {
		return domain.ProviderBinding{}, err
	}
	request = cloneStartRequest(request)
	verified, ok := starter.commands[request.Provider]
	if !ok {
		return domain.ProviderBinding{}, fmt.Errorf("provider %q has no configured command", request.Provider)
	}
	if err := verifyExecutableIdentity(verified.spec.Path, verified.identity); err != nil {
		return domain.ProviderBinding{}, fmt.Errorf("provider %q command changed after configuration: %w", request.Provider, err)
	}
	commandSpec := verified.spec
	if err := ctx.Err(); err != nil {
		return domain.ProviderBinding{}, err
	}

	generation := uint64(1)
	if request.Mode == app.SessionStartResume {
		if request.PriorBinding.Generation == ^uint64(0) {
			return domain.ProviderBinding{}, errors.New("provider launch generation exhausted")
		}
		generation = request.PriorBinding.Generation + 1
	}

	command := exec.Command(commandSpec.Path, commandSpec.Args...)
	command.Dir = request.Workdir
	command.Env = append([]string(nil), commandSpec.Env...)
	command.Env = append(command.Env,
		"BRIA_SESSION_ID="+string(request.SessionID),
		"BRIA_PROVIDER="+string(request.Provider),
		EnvironmentStartMode+"="+string(request.Mode),
		EnvironmentGeneration+"="+strconv.FormatUint(generation, 10),
	)
	if commandSpec.ProviderCredentialFile != "" {
		command.Env = append(command.Env, EnvironmentProviderCredentialFile+"="+commandSpec.ProviderCredentialFile)
	}
	if request.Mode == app.SessionStartResume {
		command.Env = append(command.Env, EnvironmentProviderSession+"="+request.PriorBinding.SessionID)
	}
	if err := processgroup.Configure(command); err != nil {
		return domain.ProviderBinding{}, fmt.Errorf("configure provider process tree: %w", err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return domain.ProviderBinding{}, fmt.Errorf("open provider stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return domain.ProviderBinding{}, fmt.Errorf("open provider stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return domain.ProviderBinding{}, fmt.Errorf("open provider stderr: %w", err)
	}

	starter.mu.Lock()
	if _, exists := starter.processes[request.SessionID]; exists {
		starter.mu.Unlock()
		return domain.ProviderBinding{}, fmt.Errorf("%w: %q", ErrSessionAlreadyTracked, request.SessionID)
	}
	if previous, exists := starter.tombstones[request.SessionID]; exists {
		if !sameLogicalRequest(previous.request, request) {
			starter.mu.Unlock()
			return domain.ProviderBinding{}, fmt.Errorf("%w with different immutable request: %q", ErrSessionAlreadyTracked, request.SessionID)
		}
		if request.Mode != app.SessionStartResume || *request.PriorBinding != previous.binding {
			starter.mu.Unlock()
			return domain.ProviderBinding{}, fmt.Errorf("%w: resume does not match prior generation for %q", ErrBindingMismatch, request.SessionID)
		}
	}
	record := &processRecord{
		request: request, command: command, stdin: stdin,
		output: make(chan wireResult, 32), outputEOF: make(chan struct{}),
		readerStop: make(chan struct{}), done: make(chan struct{}),
		generation: generation,
	}
	starter.processes[request.SessionID] = record
	starter.mu.Unlock()

	if err := command.Start(); err != nil {
		starter.removeFailedRecord(request.SessionID, record)
		return domain.ProviderBinding{}, fmt.Errorf("start provider process: %w", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	go readOutput(stdout, starter.maxLineBytes, record.output, record.outputEOF, record.readerStop)
	go starter.reapOnOutputEnd(record)

	timer := time.NewTimer(starter.handshakeTimeout)
	defer timer.Stop()
	var first wireResult
	select {
	case result, open := <-record.output:
		if !open {
			starter.killWaitAndRemove(record)
			return domain.ProviderBinding{}, errors.New("provider process exited before readiness")
		}
		first = result
	case <-record.done:
		starter.removeFailedRecord(request.SessionID, record)
		return domain.ProviderBinding{}, errors.New("provider process exited before readiness")
	case <-timer.C:
		starter.killWaitAndRemove(record)
		return domain.ProviderBinding{}, fmt.Errorf("provider readiness timed out after %s", starter.handshakeTimeout)
	case <-ctx.Done():
		starter.killWaitAndRemove(record)
		return domain.ProviderBinding{}, fmt.Errorf("wait for provider readiness: %w", ctx.Err())
	}
	if first.err != nil || validateReady(first.message) != nil ||
		(request.Mode == app.SessionStartResume && first.message.ProviderSessionID != request.PriorBinding.SessionID) {
		starter.killWaitAndRemove(record)
		return domain.ProviderBinding{}, fmt.Errorf("%w: invalid readiness message", ErrProtocol)
	}

	binding := domain.ProviderBinding{
		Provider: request.Provider, SessionID: first.message.ProviderSessionID,
		Generation: record.generation,
	}
	starter.mu.Lock()
	select {
	case <-record.done:
		starter.mu.Unlock()
		starter.removeFailedRecord(request.SessionID, record)
		return domain.ProviderBinding{}, errors.New("provider process exited during readiness")
	default:
		record.binding = binding
		delete(starter.tombstones, request.SessionID)
	}
	starter.mu.Unlock()
	return binding, nil
}

// Submit sends text byte-for-byte. Final is exposed only after a successful
// correlated completed response.
func (starter *Starter) Submit(ctx context.Context, sessionID domain.SessionID, text string) (TurnResult, error) {
	return starter.SubmitWithCallbacks(ctx, sessionID, text, TurnCallbacks{})
}

// SubmitWithCallbacks behaves like Submit while making ordered intermediate
// events and typed provider interactions observable before the terminal.
func (starter *Starter) SubmitWithCallbacks(ctx context.Context, sessionID domain.SessionID, text string, callbacks TurnCallbacks) (TurnResult, error) {
	return starter.submitWithCallbacks(ctx, sessionID, StructuredInput{Text: text}, callbacks)
}

func (starter *Starter) SubmitStructuredWithCallbacks(ctx context.Context, sessionID domain.SessionID, input StructuredInput, callbacks TurnCallbacks) (TurnResult, error) {
	input.Attachments = append([]LocalAttachment(nil), input.Attachments...)
	return starter.submitWithCallbacks(ctx, sessionID, input, callbacks)
}

func (starter *Starter) submitWithCallbacks(ctx context.Context, sessionID domain.SessionID, input StructuredInput, callbacks TurnCallbacks) (TurnResult, error) {
	if strings.TrimSpace(string(sessionID)) == "" {
		return TurnResult{}, errors.New("logical session id is required")
	}
	if !utf8.ValidString(input.Text) {
		return TurnResult{}, errors.New("turn text must be valid UTF-8")
	}
	if len(input.Text) > starter.maxTextBytes {
		return TurnResult{}, ErrTextTooLarge
	}
	if err := ctx.Err(); err != nil {
		return TurnResult{}, err
	}

	starter.mu.Lock()
	record, ok := starter.processes[sessionID]
	bound := ok && record.binding.SessionID != ""
	starter.mu.Unlock()
	if !bound {
		return TurnResult{}, fmt.Errorf("%w: %q", ErrSessionNotTracked, sessionID)
	}
	select {
	case <-record.done:
		return TurnResult{}, errors.New("provider process is not running")
	default:
	}

	requestID := fmt.Sprintf("r-%d", starter.requestSequence.Add(1))
	record.turnMu.Lock()
	if record.turn != nil {
		record.turnMu.Unlock()
		return TurnResult{}, fmt.Errorf("%w: %q", ErrTurnInFlight, sessionID)
	}
	turn := &activeTurn{requestID: requestID, done: make(chan struct{})}
	record.turn = turn
	record.turnMu.Unlock()

	if err := starter.writeContext(ctx, record, submitEnvelope{Protocol: ProtocolVersion, Type: "submit", RequestID: requestID, Text: input.Text, MessageID: callbacks.MessageID, Attachments: input.Attachments}); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return TurnResult{}, err
		}
		starter.killAndWait(record)
		return TurnResult{}, errors.New("send turn to provider adapter")
	}

	result := TurnResult{Events: make([]TurnEvent, 0)}
	accepted := false
	finalSeen := false
	pendingFinal := ""
	interactionCount := 0
	interactions := make(map[string]bool)
	for {
		select {
		case incoming, open := <-record.output:
			if !open {
				starter.finishTurn(record, turn, false, errors.New("provider process exited during turn"))
				return TurnResult{}, errors.New("provider process exited during turn")
			}
			if incoming.err != nil || incoming.message.RequestID != requestID {
				starter.killAndWait(record)
				return TurnResult{}, fmt.Errorf("%w: invalid or uncorrelated response", ErrProtocol)
			}
			message := incoming.message
			switch message.Type {
			case "accepted":
				if accepted || finalSeen || message.MessageID != callbacks.MessageID {
					return starter.protocolTurnFailure(record, requestID)
				}
				if callbacks.OnAccepted != nil {
					if err := callbacks.OnAccepted(message.MessageID); err != nil {
						starter.killAndWait(record)
						return TurnResult{}, ErrEventHandler
					}
				}
				accepted = true
			case "event":
				if !accepted || finalSeen || len(result.Events) >= starter.maxTurnEvents || validateEvent(message, starter.maxTextBytes) != nil {
					return starter.protocolTurnFailure(record, requestID)
				}
				event := TurnEvent{Kind: message.Kind, Text: message.Text}
				if callbacks.OnEvent != nil {
					if err := callbacks.OnEvent(event); err != nil {
						starter.killAndWait(record)
						return TurnResult{}, ErrEventHandler
					}
				}
				result.Events = append(result.Events, event)
			case "interaction_request":
				if !accepted || finalSeen || callbacks.OnInteraction == nil || message.InteractionRequest == nil ||
					interactionCount >= starter.maxTurnEvents {
					return starter.protocolTurnFailure(record, requestID)
				}
				interaction := *message.InteractionRequest
				if _, duplicate := interactions[interaction.ID]; duplicate {
					return starter.protocolTurnFailure(record, requestID)
				}
				interactions[interaction.ID] = false
				interactionCount++
				response, err := callbacks.OnInteraction(ctx, interaction)
				if err != nil {
					starter.killAndWait(record)
					return TurnResult{}, ErrInteractionHandler
				}
				if err := runtimeprotocol.ValidateResponse(interaction, response, runtimeprotocol.Limits{
					MaxLineBytes: starter.maxLineBytes, MaxTextBytes: starter.maxTextBytes,
				}); err != nil {
					return starter.protocolTurnFailure(record, requestID)
				}
				line, err := runtimeprotocol.EncodeParentLine(runtimeprotocol.ParentMessage{
					Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeInteractionResponse,
					RequestID: requestID, InteractionResponse: &response,
				}, runtimeprotocol.Limits{MaxLineBytes: starter.maxLineBytes, MaxTextBytes: starter.maxTextBytes})
				if err != nil || starter.writeEncodedContext(ctx, record, line) != nil {
					starter.killAndWait(record)
					return TurnResult{}, ErrInteractionHandler
				}
			case "interaction_response_accepted":
				acknowledged, exists := interactions[message.InteractionID]
				if !accepted || finalSeen || !exists || acknowledged || message.ProviderSessionID != record.binding.SessionID ||
					message.MessageID != callbacks.MessageID {
					return starter.protocolTurnFailure(record, requestID)
				}
				acceptance := InteractionResponseAcceptance{
					ProviderSessionID: message.ProviderSessionID, MessageID: message.MessageID, InteractionID: message.InteractionID,
				}
				if callbacks.OnInteractionResponseAccepted != nil {
					if err := callbacks.OnInteractionResponseAccepted(acceptance); err != nil {
						starter.killAndWait(record)
						return TurnResult{}, ErrInteractionAcceptanceHandler
					}
				}
				interactions[message.InteractionID] = true
			case "final":
				if !accepted || finalSeen || validateText(message.Text, starter.maxTextBytes) != nil {
					return starter.protocolTurnFailure(record, requestID)
				}
				finalSeen = true
				pendingFinal = message.Text
			case "completed":
				if !accepted || validateTerminal(message) != nil {
					starter.killAndWait(record)
					return TurnResult{}, fmt.Errorf("%w: invalid terminal response", ErrProtocol)
				}
				for _, acknowledged := range interactions {
					if !acknowledged {
						return starter.protocolTurnFailure(record, requestID)
					}
				}
				if message.Status == StatusCompleted && !finalSeen {
					starter.killAndWait(record)
					return TurnResult{}, fmt.Errorf("%w: successful turn has no final", ErrProtocol)
				}
				starter.finishTurn(record, turn, message.Status == StatusInterrupted, nil)
				result.TerminalStatus = message.Status
				result.ErrorCode = message.ErrorCode
				if message.Status != StatusCompleted {
					result.Final = ""
					return result, fmt.Errorf("%w: %s", ErrTurnFailed, result.ErrorCode)
				}
				result.Final = pendingFinal
				return result, nil
			default:
				return starter.protocolTurnFailure(record, requestID)
			}
		case <-record.done:
			starter.finishTurn(record, turn, false, errors.New("provider process exited during turn"))
			return TurnResult{}, errors.New("provider process exited during turn")
		case <-ctx.Done():
			interruptContext, cancel := context.WithTimeout(context.Background(), starter.closeTimeout)
			if err := starter.sendInterrupt(interruptContext, record, turn); err != nil {
				starter.killAndWait(record)
			}
			cancel()
			starter.drainInterrupted(record, turn, accepted, finalSeen)
			return TurnResult{}, ctx.Err()
		}
	}
}

// StopCurrent requests interruption of the active turn and returns only after
// the adapter has emitted the correlated interrupted terminal. The process may
// exit after that acknowledgement; callers can use Wait to supervise it.
func (starter *Starter) StopCurrent(ctx context.Context, sessionID domain.SessionID) error {
	if ctx == nil {
		return errors.New("stop context is required")
	}
	starter.mu.Lock()
	record, ok := starter.processes[sessionID]
	starter.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrSessionNotTracked, sessionID)
	}
	record.turnMu.Lock()
	turn := record.turn
	record.turnMu.Unlock()
	if turn == nil {
		return fmt.Errorf("%w: %q", ErrNoTurnInFlight, sessionID)
	}
	if err := starter.sendInterrupt(ctx, record, turn); err != nil {
		if errors.Is(err, ErrNoTurnInFlight) {
			return fmt.Errorf("%w: %q", ErrNoTurnInFlight, sessionID)
		}
		starter.killAndWait(record)
		return fmt.Errorf("interrupt provider turn: %w", err)
	}
	select {
	case <-turn.done:
		if turn.interruptConfirmed {
			return nil
		}
		if turn.terminalErr != nil {
			return fmt.Errorf("%w: %v", ErrInterruptUnconfirmed, turn.terminalErr)
		}
		return ErrInterruptUnconfirmed
	case <-ctx.Done():
		starter.killAndWait(record)
		return fmt.Errorf("wait for provider interruption: %w", ctx.Err())
	}
}

// Wait is a binding-aware supervisor seam. It returns only after the exact
// adapter generation has been reaped, and rejects a stale or unrelated binding.
func (starter *Starter) Wait(ctx context.Context, sessionID domain.SessionID, binding domain.ProviderBinding) error {
	if ctx == nil {
		return errors.New("wait context is required")
	}
	starter.mu.Lock()
	record, ok := starter.processes[sessionID]
	if !ok {
		tombstone, exited := starter.tombstones[sessionID]
		starter.mu.Unlock()
		if exited && tombstone.binding == binding {
			return nil
		}
		if exited {
			return fmt.Errorf("%w: %q", ErrBindingMismatch, sessionID)
		}
		return fmt.Errorf("%w: %q", ErrSessionNotTracked, sessionID)
	}
	if record.binding != binding {
		starter.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrBindingMismatch, sessionID)
	}
	done := record.done
	starter.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Abort requests graceful close, then confirms direct process exit. A child
// that ignores the grace period is killed and still waited for.
func (starter *Starter) Abort(ctx context.Context, request app.StartSessionRequest, binding domain.ProviderBinding) error {
	starter.mu.Lock()
	record, ok := starter.processes[request.SessionID]
	if !ok {
		if tombstone, exists := starter.tombstones[request.SessionID]; exists {
			if sameLogicalRequest(tombstone.request, request) && tombstone.binding == binding {
				starter.mu.Unlock()
				return nil
			}
			starter.mu.Unlock()
			return fmt.Errorf("%w: %q", ErrBindingMismatch, request.SessionID)
		}
		starter.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrSessionNotTracked, request.SessionID)
	}
	if !sameLogicalRequest(record.request, request) || record.binding != binding {
		starter.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrBindingMismatch, request.SessionID)
	}
	select {
	case <-record.done:
		starter.mu.Unlock()
		return nil
	default:
	}
	starter.mu.Unlock()

	closeContext, cancelClose := context.WithTimeout(ctx, starter.closeTimeout)
	closeErr := starter.writeContext(closeContext, record, closeEnvelope{Protocol: ProtocolVersion, Type: "close"})
	cancelClose()
	if closeErr != nil {
		starter.killAndWait(record)
		if ctx.Err() != nil {
			return fmt.Errorf("abort provider process: %w", ctx.Err())
		}
		return nil
	}
	timer := time.NewTimer(starter.closeTimeout)
	defer timer.Stop()
	var contextErr error
	select {
	case <-record.done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
		contextErr = ctx.Err()
	}
	starter.killAndWait(record)
	if contextErr != nil {
		return fmt.Errorf("abort provider process: %w", contextErr)
	}
	return nil
}

func readOutput(stdout io.ReadCloser, maxLineBytes int, output chan<- wireResult, outputEOF chan<- struct{}, stop <-chan struct{}) {
	defer close(output)
	defer close(outputEOF)
	defer stdout.Close()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, min(maxLineBytes, 4096)), maxLineBytes)
	for scanner.Scan() {
		message, err := decodeWire(scanner.Bytes())
		select {
		case output <- wireResult{message: message, err: err}:
		case <-stop:
			return
		}
		if err != nil {
			return
		}
	}
	if scanner.Err() != nil {
		select {
		case output <- wireResult{err: fmt.Errorf("%w: unreadable or oversized line", ErrProtocol)}:
		case <-stop:
		}
	}
}

func decodeWire(line []byte) (wireMessage, error) {
	decoded, err := runtimeprotocol.DecodeAdapterLine(line, runtimeprotocol.Limits{})
	if err != nil {
		return wireMessage{}, ErrProtocol
	}
	return wireMessage{
		Protocol: decoded.Protocol, Type: string(decoded.Type), ProviderSessionID: decoded.ProviderSessionID,
		Readiness: decoded.Readiness, Authentication: AuthenticationState(decoded.Authentication),
		RequestID: decoded.RequestID, MessageID: decoded.MessageID, Kind: EventKind(decoded.Kind), Text: decoded.Text,
		Status: decoded.Status, ErrorCode: decoded.ErrorCode, InteractionRequest: decoded.InteractionRequest,
		InteractionID: decoded.InteractionID,
	}, nil
}

func validRequestID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func validateReady(message wireMessage) error {
	if message.Type != "ready" || strings.TrimSpace(message.ProviderSessionID) == "" || message.Readiness != ReadinessProtocol || message.Authentication != AuthenticationUnknown {
		return ErrProtocol
	}
	return nil
}

func validateEvent(message wireMessage, maxTextBytes int) error {
	if message.Kind != EventCommentary && message.Kind != EventQuestion {
		return ErrProtocol
	}
	return validateText(message.Text, maxTextBytes)
}

func validateText(text string, maxTextBytes int) error {
	if !utf8.ValidString(text) || len(text) > maxTextBytes {
		return ErrProtocol
	}
	return nil
}

func validateTerminal(message wireMessage) error {
	switch message.Status {
	case StatusCompleted:
		if message.ErrorCode != "" {
			return ErrProtocol
		}
	case StatusFailed:
		switch message.ErrorCode {
		case ErrorAuthenticationFailed, ErrorProvider, ErrorProtocol, ErrorTransport:
		default:
			return ErrProtocol
		}
	case StatusInterrupted:
		if message.ErrorCode != ErrorInterrupted {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}

func (starter *Starter) encode(message any) ([]byte, error) {
	encoded, err := json.Marshal(message)
	if err != nil || len(encoded)+1 > starter.maxLineBytes {
		return nil, ErrProtocol
	}
	return append(encoded, '\n'), nil
}

// writeContext makes both waiting for the serialized writer and the pipe write
// cancellable. Killing the direct child closes the pipe, so the writer goroutine
// is always joined before this method returns.
func (starter *Starter) writeContext(ctx context.Context, record *processRecord, message any) error {
	encoded, err := starter.encode(message)
	if err != nil {
		return err
	}
	return starter.writeEncodedContext(ctx, record, encoded)
}

func (starter *Starter) writeEncodedContext(ctx context.Context, record *processRecord, encoded []byte) error {
	result := make(chan error, 1)
	go func() {
		record.writeMu.Lock()
		defer record.writeMu.Unlock()
		_, writeErr := record.stdin.Write(encoded)
		result <- writeErr
	}()

	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		starter.killAndWait(record)
		<-result
		return ctx.Err()
	case <-record.done:
		<-result
		return errors.New("provider process ended during adapter write")
	}
}

func (starter *Starter) protocolTurnFailure(record *processRecord, requestID string) (TurnResult, error) {
	starter.killAndWait(record)
	return TurnResult{}, fmt.Errorf("%w: invalid turn sequence", ErrProtocol)
}

func (starter *Starter) finishTurn(record *processRecord, turn *activeTurn, interrupted bool, terminalErr error) {
	record.turnMu.Lock()
	if record.turn == turn {
		turn.interruptConfirmed = interrupted
		turn.terminalErr = terminalErr
		record.turn = nil
		close(turn.done)
	}
	record.turnMu.Unlock()
}

func (starter *Starter) sendInterrupt(ctx context.Context, record *processRecord, turn *activeTurn) error {
	record.turnMu.Lock()
	if record.turn != turn {
		record.turnMu.Unlock()
		return ErrNoTurnInFlight
	}
	if turn.interruptSent {
		record.turnMu.Unlock()
		return nil
	}
	turn.interruptSent = true
	record.turnMu.Unlock()
	return starter.writeContext(ctx, record, requestEnvelope{
		Protocol: ProtocolVersion, Type: "interrupt", RequestID: turn.requestID,
	})
}

func (starter *Starter) drainInterrupted(record *processRecord, turn *activeTurn, accepted, finalSeen bool) {
	timer := time.NewTimer(starter.closeTimeout)
	defer timer.Stop()
	eventCount := 0
	for {
		select {
		case incoming, open := <-record.output:
			if !open || incoming.err != nil || incoming.message.RequestID != turn.requestID {
				starter.killAndWait(record)
				return
			}
			message := incoming.message
			switch message.Type {
			case "accepted":
				if accepted || finalSeen {
					starter.killAndWait(record)
					return
				}
				accepted = true
			case "event":
				if !accepted || finalSeen || eventCount >= starter.maxTurnEvents || validateEvent(message, starter.maxTextBytes) != nil {
					starter.killAndWait(record)
					return
				}
				eventCount++
			case "final":
				if !accepted || finalSeen || validateText(message.Text, starter.maxTextBytes) != nil {
					starter.killAndWait(record)
					return
				}
				finalSeen = true
			case "completed":
				if !accepted || message.Status != StatusInterrupted || message.ErrorCode != ErrorInterrupted {
					starter.killAndWait(record)
					return
				}
				starter.finishTurn(record, turn, true, nil)
				return
			default:
				starter.killAndWait(record)
				return
			}
		case <-record.done:
			return
		case <-timer.C:
			starter.killAndWait(record)
			return
		}
	}
}

func verifyCommand(command CommandSpec) (verifiedCommand, error) {
	if strings.TrimSpace(command.Path) == "" || !filepath.IsAbs(command.Path) {
		return verifiedCommand{}, errors.New("executable path must be absolute")
	}
	if strings.ContainsRune(command.Path, '\x00') {
		return verifiedCommand{}, errors.New("executable path contains NUL")
	}
	if isShellExecutable(command.Path) {
		return verifiedCommand{}, errors.New("executable must not be a shell or command-discovery launcher")
	}
	resolved, err := filepath.EvalSymlinks(command.Path)
	if err != nil {
		return verifiedCommand{}, errors.New("executable target must exist")
	}
	if isShellExecutable(resolved) {
		return verifiedCommand{}, errors.New("resolved executable must not be a shell or command-discovery launcher")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return verifiedCommand{}, errors.New("executable target must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return verifiedCommand{}, errors.New("executable target must be executable")
	}
	for _, argument := range command.Args {
		if strings.ContainsRune(argument, '\x00') {
			return verifiedCommand{}, errors.New("command argument contains NUL")
		}
	}
	if command.ProviderCredentialFile != "" {
		if !filepath.IsAbs(command.ProviderCredentialFile) || strings.ContainsRune(command.ProviderCredentialFile, '\x00') {
			return verifiedCommand{}, errors.New("provider credential file reference must be an absolute path")
		}
		command.ProviderCredentialFile = filepath.Clean(command.ProviderCredentialFile)
	}
	seen := make(map[string]bool, len(command.Env))
	for _, entry := range command.Env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !validEnvironmentName(name) || strings.ContainsRune(value, '\x00') {
			return verifiedCommand{}, errors.New("environment entry has invalid KEY=VALUE syntax")
		}
		if name == "BRIA_SESSION_ID" || name == "BRIA_PROVIDER" ||
			name == EnvironmentStartMode || name == EnvironmentProviderSession || name == EnvironmentGeneration ||
			name == EnvironmentProviderCredentialFile {
			return verifiedCommand{}, errors.New("environment must not override reserved Bria variables")
		}
		if seen[name] {
			return verifiedCommand{}, errors.New("environment contains duplicate keys")
		}
		seen[name] = true
	}
	return verifiedCommand{
		spec: CommandSpec{
			Path:                   resolved,
			Args:                   append([]string(nil), command.Args...),
			Env:                    append([]string(nil), command.Env...),
			ProviderCredentialFile: command.ProviderCredentialFile,
		},
		identity: info,
	}, nil
}

func verifyExecutableIdentity(path string, identity os.FileInfo) error {
	current, err := os.Stat(path)
	if err != nil || !current.Mode().IsRegular() {
		return errors.New("verified executable is unavailable")
	}
	if runtime.GOOS != "windows" && current.Mode().Perm()&0o111 == 0 {
		return errors.New("verified executable is no longer executable")
	}
	if !os.SameFile(identity, current) {
		return errors.New("verified executable identity changed")
	}
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index := range len(name) {
		character := name[index]
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func isShellExecutable(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "csh", "tcsh",
		"cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe",
		"env", "env.exe":
		return true
	default:
		return false
	}
}

func validateRequest(request app.StartSessionRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.PriorBinding != nil && (strings.ContainsRune(request.PriorBinding.SessionID, '\x00') ||
		!utf8.ValidString(request.PriorBinding.SessionID)) {
		return errors.New("prior provider session id is invalid")
	}
	return nil
}

func cloneStartRequest(request app.StartSessionRequest) app.StartSessionRequest {
	if request.PriorBinding != nil {
		binding := *request.PriorBinding
		request.PriorBinding = &binding
	}
	return request
}

func sameLogicalRequest(left, right app.StartSessionRequest) bool {
	return left.SessionID == right.SessionID &&
		left.ComputerID == right.ComputerID &&
		left.Provider == right.Provider &&
		left.Workdir == right.Workdir
}

func (starter *Starter) reapOnOutputEnd(record *processRecord) {
	<-record.outputEOF
	record.lifecycleMu.Lock()
	if record.reaping {
		record.lifecycleMu.Unlock()
		return
	}
	record.reaping = true
	// The protocol stream ended unexpectedly or because the adapter exited.
	// Signal the still-unreaped group before Wait so no PID can be reused
	// between reap and a later negative-PID signal.
	_ = processgroup.KillTree(record.command)
	_ = record.command.Wait()
	starter.finalizeReap(record)
	record.lifecycleMu.Unlock()
}

func (starter *Starter) finalizeReap(record *processRecord) {
	record.turnMu.Lock()
	if turn := record.turn; turn != nil {
		turn.terminalErr = errors.New("provider process exited before turn terminal")
		record.turn = nil
		close(turn.done)
	}
	record.turnMu.Unlock()
	starter.mu.Lock()
	if starter.processes[record.request.SessionID] == record {
		delete(starter.processes, record.request.SessionID)
		if record.binding.SessionID != "" {
			starter.tombstones[record.request.SessionID] = processTombstone{request: record.request, binding: record.binding}
		}
	}
	starter.mu.Unlock()
	close(record.done)
}

func (starter *Starter) killAndWait(record *processRecord) {
	record.lifecycleMu.Lock()
	if record.reaping {
		record.lifecycleMu.Unlock()
		<-record.done
		return
	}
	record.reaping = true

	terminateErr := processgroup.TerminateTree(record.command)
	timer := time.NewTimer(starter.terminateTimeout)
	if terminateErr == nil {
		select {
		case <-record.outputEOF:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			record.readerStopOnce.Do(func() { close(record.readerStop) })
			_ = processgroup.KillTree(record.command)
			<-record.outputEOF
		}
	} else {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		record.readerStopOnce.Do(func() { close(record.readerStop) })
		_ = processgroup.KillTree(record.command)
		<-record.outputEOF
	}
	// KillTree is intentionally before Wait even after clean TERM exit: it
	// clears any same-group stragglers while the leader PID is still unreaped.
	_ = processgroup.KillTree(record.command)
	_ = record.command.Wait()
	starter.finalizeReap(record)
	record.lifecycleMu.Unlock()
}

func (starter *Starter) killWaitAndRemove(record *processRecord) {
	starter.killAndWait(record)
	starter.removeFailedRecord(record.request.SessionID, record)
}

func (starter *Starter) removeFailedRecord(sessionID domain.SessionID, record *processRecord) {
	starter.mu.Lock()
	if starter.processes[sessionID] == record {
		delete(starter.processes, sessionID)
	}
	starter.mu.Unlock()
}
