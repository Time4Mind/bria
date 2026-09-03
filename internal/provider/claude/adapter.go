package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bria/internal/runtimeprotocol"
)

const AdapterProtocolVersion = 1

const (
	ReadinessProtocol     = "protocol"
	AuthenticationUnknown = "unknown"

	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"

	ErrorAuthenticationFailed = "authentication_failed"
	ErrorProvider             = "provider_error"
	ErrorProtocol             = "protocol_error"
	ErrorTransport            = "transport_error"
	ErrorInterrupted          = "interrupted"

	defaultAdapterMaxLineBytes = 64 << 10
	defaultAdapterMaxTextBytes = 32 << 10
	defaultAdapterStopTimeout  = 5 * time.Second
)

var (
	ErrInvalidCommand   = errors.New("invalid claude command")
	ErrUnsafeArgument   = errors.New("unsafe claude argument")
	ErrAdapterProtocol  = errors.New("claude adapter protocol violation")
	ErrAdapterTransport = errors.New("claude adapter transport failed")
	ErrChildStart       = errors.New("claude child failed to start")
	ErrChildExit        = errors.New("claude child exited unexpectedly")
	ErrChildStop        = errors.New("claude child termination was not confirmed")
)

type CommandSpec struct {
	Path           string
	Args           []string
	Workdir        string
	SessionID      string
	executableInfo os.FileInfo
	mode           commandSessionMode
}

type commandSessionMode uint8

const (
	commandSessionNew commandSessionMode = iota + 1
	commandSessionResume
)

func BuildCommandSpec(path string, rawArgs []string, workdir string, random io.Reader) (CommandSpec, error) {
	for _, argument := range rawArgs {
		name, _, _ := strings.Cut(argument, "=")
		if name == "--session-id" {
			return CommandSpec{}, ErrUnsafeArgument
		}
	}
	return buildCommandSpec(path, rawArgs, workdir, random, "")
}

// BuildResumeCommandSpec constructs a Claude invocation which must continue
// the exact persisted provider session. It never generates a replacement ID
// and keeps all session-selection flags under Bria's control.
func BuildResumeCommandSpec(path string, rawArgs []string, workdir, sessionID string) (CommandSpec, error) {
	if !isCanonicalUUID(sessionID) {
		return CommandSpec{}, ErrInvalidCommand
	}
	return buildCommandSpec(path, rawArgs, workdir, nil, sessionID)
}

func buildCommandSpec(path string, rawArgs []string, workdir string, random io.Reader, resumeSessionID string) (CommandSpec, error) {
	if !filepath.IsAbs(path) || !filepath.IsAbs(workdir) || strings.ContainsRune(path, '\x00') {
		return CommandSpec{}, ErrInvalidCommand
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolvedPath) {
		return CommandSpec{}, ErrInvalidCommand
	}
	executableInfo, err := os.Stat(resolvedPath)
	if err != nil || !secureClaudeExecutable(executableInfo) {
		return CommandSpec{}, ErrInvalidCommand
	}

	args := make([]string, 0, len(rawArgs)+10)
	sessionID := resumeSessionID
	mode := commandSessionNew
	if resumeSessionID != "" {
		mode = commandSessionResume
	}
	for index := 0; index < len(rawArgs); index++ {
		argument := rawArgs[index]
		if argument == "--" || !strings.HasPrefix(argument, "-") {
			return CommandSpec{}, ErrUnsafeArgument
		}
		name, inlineValue, hasInlineValue := strings.Cut(argument, "=")
		switch name {
		case "--dangerously-skip-permissions", "--allow-dangerously-skip-permissions", "--debug-file",
			"--debug", "-d", "--bg", "--exec",
			"--input-format", "--output-format", "--print", "-p", "--verbose",
			"--replay-user-messages", "--resume", "-r", "--continue", "-c",
			"--fork-session", "--no-session-persistence", "--permission-prompt-tool":
			return CommandSpec{}, ErrUnsafeArgument
		case "--permission-mode":
			value := inlineValue
			if !hasInlineValue {
				if index+1 >= len(rawArgs) {
					return CommandSpec{}, ErrInvalidCommand
				}
				index++
				value = rawArgs[index]
				args = append(args, argument, value)
			} else {
				args = append(args, argument)
			}
			if value != "default" && value != "plan" && value != "dontAsk" {
				return CommandSpec{}, ErrUnsafeArgument
			}
			continue
		case "--allowedTools", "--allowed-tools", "--tools":
			return CommandSpec{}, ErrUnsafeArgument
		case "--model", "--fallback-model", "--effort":
			return CommandSpec{}, ErrUnsafeArgument
		case "--disallowedTools", "--disallowed-tools", "--max-budget-usd",
			"--max-turns", "--system-prompt", "--append-system-prompt", "--betas":
			if !hasInlineValue {
				if index+1 >= len(rawArgs) || strings.HasPrefix(rawArgs[index+1], "-") {
					return CommandSpec{}, ErrInvalidCommand
				}
				index++
				args = append(args, argument, rawArgs[index])
			} else if inlineValue == "" {
				return CommandSpec{}, ErrInvalidCommand
			} else {
				args = append(args, argument)
			}
			continue
		case "--session-id":
			if mode == commandSessionResume || sessionID != "" {
				return CommandSpec{}, ErrUnsafeArgument
			}
			value := inlineValue
			if !hasInlineValue {
				if index+1 >= len(rawArgs) {
					return CommandSpec{}, ErrInvalidCommand
				}
				index++
				value = rawArgs[index]
			}
			if !isCanonicalUUID(value) {
				return CommandSpec{}, ErrInvalidCommand
			}
			sessionID = value
			continue
		default:
			return CommandSpec{}, ErrUnsafeArgument
		}
	}
	if sessionID == "" {
		generated, err := generateUUID(random)
		if err != nil {
			return CommandSpec{}, ErrInvalidCommand
		}
		sessionID = generated
	}

	args = append(args,
		"--bare",
		"--print",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--replay-user-messages",
		"--permission-prompt-tool", "stdio",
	)
	if mode == commandSessionResume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	return CommandSpec{
		Path:           filepath.Clean(resolvedPath),
		Args:           args,
		Workdir:        filepath.Clean(workdir),
		SessionID:      sessionID,
		executableInfo: executableInfo,
		mode:           mode,
	}, nil
}

func generateUUID(random io.Reader) (string, error) {
	if random == nil {
		return "", ErrInvalidCommand
	}
	var value [16]byte
	if _, err := io.ReadFull(random, value[:]); err != nil {
		return "", ErrInvalidCommand
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}

func isCanonicalUUID(value string) bool {
	if len(value) != 36 || value != strings.ToLower(value) ||
		value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	if len(compact) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(compact)
	return err == nil && len(decoded) == 16
}

type ChildProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Done() <-chan error
	Kill() error
}

type ProcessFactory interface {
	Start(context.Context, CommandSpec) (ChildProcess, error)
}

type AdapterOptions struct {
	MaxLineBytes int
	MaxTextBytes int
	StopTimeout  time.Duration
}

type Adapter struct {
	spec    CommandSpec
	input   io.ReadCloser
	output  io.Writer
	factory ProcessFactory
	options AdapterOptions
}

func NewAdapter(
	spec CommandSpec,
	input io.ReadCloser,
	output io.Writer,
	factory ProcessFactory,
	options AdapterOptions,
) (*Adapter, error) {
	if input == nil || output == nil || factory == nil || !validBuiltCommand(spec) {
		return nil, ErrInvalidCommand
	}
	if options.MaxLineBytes == 0 {
		options.MaxLineBytes = defaultAdapterMaxLineBytes
	}
	if options.MaxTextBytes == 0 {
		options.MaxTextBytes = defaultAdapterMaxTextBytes
	}
	if options.StopTimeout == 0 {
		options.StopTimeout = defaultAdapterStopTimeout
	}
	if options.MaxLineBytes < 1 || options.MaxTextBytes < 1 || options.StopTimeout < 0 {
		return nil, ErrInvalidCommand
	}
	return &Adapter{
		spec:    cloneCommandSpec(spec),
		input:   input,
		output:  output,
		factory: factory,
		options: options,
	}, nil
}

func (adapter *Adapter) Run(ctx context.Context) error {
	if ctx == nil {
		return ErrAdapterProtocol
	}
	child, err := adapter.factory.Start(ctx, cloneCommandSpec(adapter.spec))
	if err != nil || child == nil {
		if child != nil {
			_ = child.Kill()
		}
		return ErrChildStart
	}
	childInput := child.Stdin()
	childOutput := child.Stdout()
	childDone := child.Done()
	if childInput == nil || childOutput == nil || childDone == nil {
		_ = child.Kill()
		return ErrChildStart
	}
	client, err := NewClient(
		childOutput,
		childInput,
		Options{MaxEventBytes: adapter.options.MaxLineBytes},
	)
	if err != nil {
		_ = child.Kill()
		return ErrChildStart
	}
	controller := childController{
		child:   child,
		client:  client,
		done:    childDone,
		timeout: adapter.options.StopTimeout,
	}

	if err := adapter.write(readyOutput{
		Protocol:          AdapterProtocolVersion,
		Type:              "ready",
		ProviderSessionID: adapter.spec.SessionID,
		Readiness:         ReadinessProtocol,
		Authentication:    AuthenticationUnknown,
	}); err != nil {
		_ = controller.stop()
		return err
	}

	loopContext, cancel := context.WithCancel(ctx)
	commands := make(chan commandResult, 1)
	providerEvents := make(chan providerResult, 1)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		adapter.readCommands(loopContext, commands)
	}()
	go func() {
		defer readers.Done()
		readProviderEvents(loopContext, client, providerEvents)
	}()

	runErr, stopped := adapter.runLoop(loopContext, client, &controller, commands, providerEvents)
	cancel()
	_ = adapter.input.Close()
	_ = client.Close()
	if !stopped {
		if stopErr := controller.stop(); runErr == nil && stopErr != nil {
			runErr = stopErr
		}
	}
	readers.Wait()
	return runErr
}

type activeTurn struct {
	requestID     string
	messageID     string
	accepted      bool
	assistantSeen bool
	permission    *PermissionRequest
}

func (adapter *Adapter) runLoop(
	ctx context.Context,
	client *Client,
	controller *childController,
	commands <-chan commandResult,
	providerEvents <-chan providerResult,
) (error, bool) {
	var active *activeTurn
	queued := make([]parentCommand, 0, 8)
	initialized := false
	// Once the provider exits, its stdout may still contain events already
	// read by readProviderEvents.  Disable the closed done channel and drain
	// that ordered event stream before treating EOF as a transport failure.
	done := controller.done
	var exitGrace <-chan time.Time
	completedRequests := make(map[string]struct{})
	completedOrder := make([]string, 0, 128)
	rememberCompleted := func(requestID string) {
		if _, exists := completedRequests[requestID]; exists {
			return
		}
		const historyLimit = 128
		if len(completedOrder) == historyLimit {
			delete(completedRequests, completedOrder[0])
			copy(completedOrder, completedOrder[1:])
			completedOrder = completedOrder[:historyLimit-1]
		}
		completedRequests[requestID] = struct{}{}
		completedOrder = append(completedOrder, requestID)
	}
	isOutstanding := func(requestID string) bool {
		if active != nil && active.requestID == requestID {
			return true
		}
		for _, pending := range queued {
			if pending.requestID == requestID {
				return true
			}
		}
		return false
	}
	startSubmit := func(command parentCommand) error {
		active = &activeTurn{requestID: command.requestID, messageID: command.messageID}
		var err error
		if command.messageID == "" {
			err = client.SendUser(command.text)
		} else {
			err = client.SendUserWithID(command.text, command.messageID)
		}
		if err != nil {
			_ = adapter.completed(active.requestID, StatusFailed, ErrorTransport)
			return ErrAdapterTransport
		}
		return nil
	}
	startQueued := func() error {
		if active != nil || len(queued) == 0 {
			return nil
		}
		next := queued[0]
		copy(queued, queued[1:])
		queued = queued[:len(queued)-1]
		return startSubmit(next)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err(), false
		case processErr, ok := <-done:
			if !ok {
				processErr = nil
			}
			controller.exited = true
			done = nil
			timer := time.NewTimer(100 * time.Millisecond)
			exitGrace = timer.C
			_ = processErr
			// Keep processing providerEvents. The child can terminate immediately
			// after writing a terminal result (notably on authentication failure).
			// Returning here races that result and loses the real error code.
		case <-exitGrace:
			if active != nil {
				_ = adapter.completed(active.requestID, StatusFailed, ErrorTransport)
				return ErrChildExit, true
			}
			return nil, true
		case result := <-commands:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return controller.stop(), true
				}
				if errors.Is(result.err, ErrAdapterTransport) {
					return ErrAdapterTransport, false
				}
				return ErrAdapterProtocol, false
			}
			switch result.command.kind {
			case commandSubmit:
				if _, duplicate := completedRequests[result.command.requestID]; duplicate || isOutstanding(result.command.requestID) {
					return adapter.failAndStop(controller, result.command.requestID, ErrorProtocol)
				}
				if active != nil {
					queued = append(queued, result.command)
					continue
				}
				if err := startSubmit(result.command); err != nil {
					_ = controller.stop()
					return ErrAdapterTransport, true
				}
			case commandInterrupt:
				if _, alreadyCompleted := completedRequests[result.command.requestID]; alreadyCompleted {
					continue
				}
				if active == nil || active.requestID != result.command.requestID {
					return adapter.failAndStop(controller, result.command.requestID, ErrorProtocol)
				}
				if err := controller.stop(); err != nil {
					return err, true
				}
				if err := adapter.completed(active.requestID, StatusInterrupted, ErrorInterrupted); err != nil {
					return err, true
				}
				rememberCompleted(active.requestID)
				return nil, true
			case commandClose:
				return controller.stop(), true
			case commandInteractionResponse:
				if active == nil || active.permission == nil || result.command.interactionResponse == nil {
					return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
				}
				interaction, err := claudePermissionInteraction(active.requestID, *active.permission)
				if err != nil || runtimeprotocol.ValidateResponse(interaction, *result.command.interactionResponse, runtimeprotocol.Limits{MaxLineBytes: adapter.options.MaxLineBytes, MaxTextBytes: adapter.options.MaxTextBytes}) != nil {
					return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
				}
				decision := PermissionDeny
				switch result.command.interactionResponse.Decision {
				case runtimeprotocol.DecisionAccept:
					decision = PermissionAllow
				case runtimeprotocol.DecisionDecline:
					decision = PermissionDeny
				case runtimeprotocol.DecisionCancel:
					decision = PermissionDenyAndInterrupt
				default:
					return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
				}
				if err := client.RespondPermission(ctx, active.permission.RequestID, decision); err != nil {
					return adapter.failCurrentAndStop(controller, active, ErrorTransport)
				}
				if err := adapter.interactionResponseAccepted(active, interaction.ID); err != nil {
					return err, false
				}
				active.permission = nil
			default:
				return ErrAdapterProtocol, false
			}
		case result := <-providerEvents:
			if result.err != nil {
				if active == nil && controller.exited && errors.Is(result.err, io.EOF) {
					return nil, true
				}
				code := ErrorProtocol
				adapterErr := ErrAdapterProtocol
				if errors.Is(result.err, ErrTransport) || errors.Is(result.err, io.EOF) {
					code = ErrorTransport
					adapterErr = ErrAdapterTransport
				}
				if active != nil {
					_ = adapter.completed(active.requestID, StatusFailed, code)
				}
				_ = controller.stop()
				return adapterErr, true
			}
			event := result.event
			switch event.Kind {
			case EventUnknown:
				continue
			case EventInit:
				if active == nil || initialized || event.SessionID != adapter.spec.SessionID ||
					event.Init == nil || event.Init.CWD != adapter.spec.Workdir {
					if active == nil {
						_ = controller.stop()
						return ErrAdapterProtocol, true
					}
					return adapter.failAndStop(controller, active.requestID, ErrorProtocol)
				}
				initialized = true
			case EventUserReplay:
				if !initialized || active == nil || active.accepted {
					return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
				}
				if err := adapter.write(acceptedOutput{
					Protocol:  AdapterProtocolVersion,
					Type:      "accepted",
					RequestID: active.requestID,
					MessageID: active.messageID,
				}); err != nil {
					_ = controller.stop()
					return err, true
				}
				active.accepted = true
			case EventInternalUser:
				if active == nil || !active.accepted {
					return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
				}
			case EventAssistant:
				if active == nil || !active.accepted || event.Assistant == nil {
					return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
				}
				active.assistantSeen = true
				if event.Assistant.Text != "" {
					if err := adapter.commentary(active.requestID, event.Assistant.Text); err != nil {
						return adapter.failCurrentAndStop(controller, active, ErrorProvider)
					}
					if controller.observeExit() {
						_ = adapter.completed(active.requestID, StatusFailed, ErrorTransport)
						rememberCompleted(active.requestID)
						return ErrChildExit, true
					}
				}
				for _, tool := range event.Assistant.Tools {
					if err := adapter.commentary(active.requestID, "tool: "+tool); err != nil {
						return adapter.failCurrentAndStop(controller, active, ErrorProvider)
					}
					if controller.observeExit() {
						_ = adapter.completed(active.requestID, StatusFailed, ErrorTransport)
						rememberCompleted(active.requestID)
						return ErrChildExit, true
					}
				}
			case EventPermissionRequest:
				if active == nil || !active.accepted || active.permission != nil || event.Permission == nil {
					return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
				}
				interaction, err := claudePermissionInteraction(active.requestID, *event.Permission)
				if err != nil || adapter.writeInteraction(active.requestID, interaction) != nil {
					return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
				}
				active.permission = event.Permission
			case EventResult:
				if active == nil || !active.accepted || !active.assistantSeen || active.permission != nil || event.Result == nil {
					return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
				}
				if event.Result.FailureCode == FailureAuthentication {
					if err := adapter.completed(active.requestID, StatusFailed, ErrorAuthenticationFailed); err != nil {
						return err, false
					}
					rememberCompleted(active.requestID)
					active = nil
					if err := startQueued(); err != nil {
						_ = controller.stop()
						return err, true
					}
					continue
				}
				if !event.Result.TerminalSuccess(nil) {
					if err := adapter.completed(active.requestID, StatusFailed, ErrorProvider); err != nil {
						return err, false
					}
					rememberCompleted(active.requestID)
					active = nil
					if err := startQueued(); err != nil {
						_ = controller.stop()
						return err, true
					}
					continue
				}
				finalFrame := finalOutput{
					Protocol: AdapterProtocolVersion, Type: "final", RequestID: active.requestID,
					Text: event.Result.Text,
				}
				if len(event.Result.Text) > adapter.options.MaxTextBytes || !adapter.frameFits(finalFrame) {
					if err := adapter.completed(active.requestID, StatusFailed, ErrorProvider); err != nil {
						return err, false
					}
					rememberCompleted(active.requestID)
					active = nil
					if err := startQueued(); err != nil {
						_ = controller.stop()
						return err, true
					}
					continue
				}
				if controller.observeExit() {
					_ = adapter.completed(active.requestID, StatusFailed, ErrorTransport)
					rememberCompleted(active.requestID)
					return ErrChildExit, true
				}
				if err := adapter.write(finalFrame); err != nil {
					return err, false
				}
				if controller.observeExit() {
					_ = adapter.completed(active.requestID, StatusFailed, ErrorTransport)
					rememberCompleted(active.requestID)
					return ErrChildExit, true
				}
				if err := adapter.completed(active.requestID, StatusCompleted, ""); err != nil {
					return err, false
				}
				rememberCompleted(active.requestID)
				active = nil
				if err := startQueued(); err != nil {
					_ = controller.stop()
					return err, true
				}
			default:
				return adapter.failCurrentAndStop(controller, active, ErrorProtocol)
			}
		}
	}
}

func (adapter *Adapter) failAndStop(
	controller *childController,
	requestID string,
	code string,
) (error, bool) {
	if validRequestID(requestID) {
		_ = adapter.completed(requestID, StatusFailed, code)
	}
	_ = controller.stop()
	return ErrAdapterProtocol, true
}

func (adapter *Adapter) failCurrentAndStop(
	controller *childController,
	active *activeTurn,
	code string,
) (error, bool) {
	if active != nil {
		_ = adapter.completed(active.requestID, StatusFailed, code)
	}
	_ = controller.stop()
	return ErrAdapterProtocol, true
}

func (adapter *Adapter) completed(requestID, status, errorCode string) error {
	return adapter.write(completedOutput{
		Protocol:  AdapterProtocolVersion,
		Type:      "completed",
		RequestID: requestID,
		Status:    status,
		ErrorCode: errorCode,
	})
}

func (adapter *Adapter) commentary(requestID, message string) error {
	frame := eventOutput{
		Protocol: AdapterProtocolVersion, Type: "event", RequestID: requestID,
		Kind: "commentary", Text: message,
	}
	if len(message) > adapter.options.MaxTextBytes || !adapter.frameFits(frame) {
		return ErrAdapterProtocol
	}
	return adapter.write(frame)
}

type childController struct {
	child   ChildProcess
	client  *Client
	done    <-chan error
	timeout time.Duration
	exited  bool
}

func (controller *childController) observeExit() bool {
	if controller.exited {
		return true
	}
	select {
	case <-controller.done:
		controller.exited = true
		return true
	default:
		return false
	}
}

func (controller *childController) stop() error {
	if controller.exited {
		return nil
	}
	_ = controller.client.Close()
	_ = controller.child.Kill()
	timer := time.NewTimer(controller.timeout)
	defer timer.Stop()
	select {
	case <-controller.done:
		controller.exited = true
		return nil
	case <-timer.C:
		return ErrChildStop
	}
}

type commandKind uint8

const (
	commandSubmit commandKind = iota + 1
	commandInterrupt
	commandClose
	commandInteractionResponse
)

type parentCommand struct {
	kind                commandKind
	requestID           string
	text                string
	messageID           string
	interactionResponse *runtimeprotocol.InteractionResponse
}

type commandResult struct {
	command parentCommand
	err     error
}

type providerResult struct {
	event Event
	err   error
}

func (adapter *Adapter) readCommands(ctx context.Context, output chan<- commandResult) {
	reader := bufio.NewReader(adapter.input)
	for {
		line, err := readBoundedLine(reader, adapter.options.MaxLineBytes)
		if err != nil {
			sendCommandResult(ctx, output, commandResult{err: err})
			return
		}
		command, err := decodeParentCommand(line, adapter.options.MaxTextBytes)
		if !sendCommandResult(ctx, output, commandResult{command: command, err: err}) || err != nil {
			return
		}
	}
}

func sendCommandResult(ctx context.Context, output chan<- commandResult, result commandResult) bool {
	select {
	case output <- result:
		return true
	case <-ctx.Done():
		return false
	}
}

func readProviderEvents(ctx context.Context, client *Client, output chan<- providerResult) {
	for {
		event, err := client.NextEvent()
		select {
		case output <- providerResult{event: event, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func decodeParentCommand(line []byte, maxTextBytes int) (parentCommand, error) {
	if !utf8.Valid(line) {
		return parentCommand{}, ErrAdapterProtocol
	}
	decoded, err := runtimeprotocol.DecodeParentLine(line, runtimeprotocol.Limits{MaxTextBytes: maxTextBytes})
	if err != nil {
		return parentCommand{}, ErrAdapterProtocol
	}
	switch decoded.Type {
	case runtimeprotocol.TypeSubmit:
		if len(decoded.Attachments) != 0 {
			return parentCommand{}, ErrAdapterProtocol
		}
		return parentCommand{kind: commandSubmit, requestID: decoded.RequestID, messageID: decoded.MessageID, text: decoded.Text}, nil
	case runtimeprotocol.TypeInterrupt:
		return parentCommand{kind: commandInterrupt, requestID: decoded.RequestID}, nil
	case runtimeprotocol.TypeClose:
		return parentCommand{kind: commandClose}, nil
	case runtimeprotocol.TypeInteractionResponse:
		return parentCommand{kind: commandInteractionResponse, requestID: decoded.RequestID, interactionResponse: decoded.InteractionResponse}, nil
	default:
		return parentCommand{}, ErrAdapterProtocol
	}
}

func decodeObject(line []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrAdapterProtocol
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return nil, ErrAdapterProtocol
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, ErrAdapterProtocol
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrAdapterProtocol
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrAdapterProtocol
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, ErrAdapterProtocol
	}
	return fields, nil
}

func exactKeys(fields map[string]json.RawMessage, expected ...string) bool {
	if len(fields) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func validRequestID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func readBoundedLine(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	line := make([]byte, 0, min(maxBytes, 4096))
	for {
		fragment, err := reader.ReadSlice('\n')
		line = append(line, fragment...)
		payloadLength := len(line)
		if payloadLength > 0 && line[payloadLength-1] == '\n' {
			payloadLength--
			if payloadLength > 0 && line[payloadLength-1] == '\r' {
				payloadLength--
			}
		}
		if payloadLength > maxBytes {
			return nil, ErrAdapterProtocol
		}
		switch {
		case err == nil:
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				return nil, ErrAdapterProtocol
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return line, nil
		case errors.Is(err, io.EOF):
			return nil, io.EOF
		default:
			return nil, ErrAdapterTransport
		}
	}
}

func (adapter *Adapter) write(value any) error {
	document, err := json.Marshal(value)
	if err != nil {
		return ErrAdapterProtocol
	}
	if len(document) > adapter.options.MaxLineBytes {
		return ErrAdapterProtocol
	}
	document = append(document, '\n')
	if err := writeAll(adapter.output, document); err != nil {
		return ErrAdapterTransport
	}
	return nil
}

func (adapter *Adapter) writeInteraction(requestID string, interaction runtimeprotocol.InteractionRequest) error {
	document, err := runtimeprotocol.EncodeAdapterLine(runtimeprotocol.AdapterMessage{
		Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeInteractionRequest,
		RequestID: requestID, InteractionRequest: &interaction,
	}, runtimeprotocol.Limits{MaxLineBytes: adapter.options.MaxLineBytes, MaxTextBytes: adapter.options.MaxTextBytes})
	if err != nil {
		return ErrAdapterProtocol
	}
	if err := writeAll(adapter.output, document); err != nil {
		return ErrAdapterTransport
	}
	return nil
}

func (adapter *Adapter) interactionResponseAccepted(active *activeTurn, interactionID string) error {
	if active == nil || active.requestID == "" || active.messageID == "" || interactionID == "" {
		return ErrAdapterProtocol
	}
	document, err := runtimeprotocol.EncodeAdapterLine(runtimeprotocol.AdapterMessage{
		Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeInteractionResponseAccepted,
		ProviderSessionID: adapter.spec.SessionID, RequestID: active.requestID,
		MessageID: active.messageID, InteractionID: interactionID,
	}, runtimeprotocol.Limits{MaxLineBytes: adapter.options.MaxLineBytes, MaxTextBytes: adapter.options.MaxTextBytes})
	if err != nil {
		return ErrAdapterProtocol
	}
	if err := writeAll(adapter.output, document); err != nil {
		return ErrAdapterTransport
	}
	return nil
}

func claudePermissionInteraction(turnID string, permission PermissionRequest) (runtimeprotocol.InteractionRequest, error) {
	result := runtimeprotocol.InteractionRequest{
		ID: permission.RequestID, TurnID: turnID, ItemID: permission.ToolUseID,
		Reason:    firstNonBlank(permission.Description, permission.Title),
		Decisions: []runtimeprotocol.ApprovalDecision{runtimeprotocol.DecisionAccept, runtimeprotocol.DecisionDecline, runtimeprotocol.DecisionCancel},
	}
	if permission.Command != "" {
		result.Kind = runtimeprotocol.InteractionCommandApproval
		result.Command = permission.Command
		result.Cwd = permission.CWD
	} else if permission.Path != "" {
		result.Kind = runtimeprotocol.InteractionFileApproval
		result.GrantRoot = permission.Path
		result.Reason = firstNonBlank(result.Reason, permission.ToolName)
	} else {
		result.Kind = runtimeprotocol.InteractionCommandApproval
		result.Command = permission.ToolName
	}
	if _, err := runtimeprotocol.EncodeAdapterLine(runtimeprotocol.AdapterMessage{Protocol: 1, Type: runtimeprotocol.TypeInteractionRequest, RequestID: turnID, InteractionRequest: &result}, runtimeprotocol.Limits{}); err != nil {
		return runtimeprotocol.InteractionRequest{}, ErrAdapterProtocol
	}
	return result, nil
}

func (adapter *Adapter) frameFits(value any) bool {
	document, err := json.Marshal(value)
	return err == nil && len(document) <= adapter.options.MaxLineBytes
}

type readyOutput struct {
	Protocol          int    `json:"protocol"`
	Type              string `json:"type"`
	ProviderSessionID string `json:"provider_session_id"`
	Readiness         string `json:"readiness"`
	Authentication    string `json:"authentication"`
}

type acceptedOutput struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	MessageID string `json:"message_id,omitempty"`
}

type finalOutput struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Text      string `json:"text"`
}

type eventOutput struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
}

type completedOutput struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

func validBuiltCommand(spec CommandSpec) bool {
	if !filepath.IsAbs(spec.Path) || !filepath.IsAbs(spec.Workdir) || !isCanonicalUUID(spec.SessionID) || spec.executableInfo == nil ||
		(spec.mode != commandSessionNew && spec.mode != commandSessionResume) {
		return false
	}
	wantSuffix := []string{
		"--bare",
		"--print", "--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--replay-user-messages",
		"--permission-prompt-tool", "stdio",
	}
	if spec.mode == commandSessionResume {
		wantSuffix = append(wantSuffix, "--resume", spec.SessionID)
	} else {
		wantSuffix = append(wantSuffix, "--session-id", spec.SessionID)
	}
	if len(spec.Args) < len(wantSuffix) {
		return false
	}
	gotSuffix := spec.Args[len(spec.Args)-len(wantSuffix):]
	for index := range wantSuffix {
		if gotSuffix[index] != wantSuffix[index] {
			return false
		}
	}
	prefix := spec.Args[:len(spec.Args)-len(wantSuffix)]
	var probe CommandSpec
	var err error
	if spec.mode == commandSessionResume {
		probe, err = BuildResumeCommandSpec(spec.Path, append([]string(nil), prefix...), spec.Workdir, spec.SessionID)
	} else {
		probe, err = buildCommandSpec(spec.Path, append(append([]string(nil), prefix...), "--session-id", spec.SessionID), spec.Workdir, bytes.NewReader(nil), "")
	}
	if err != nil {
		return false
	}
	return strings.Join(probe.Args, "\x00") == strings.Join(spec.Args, "\x00")
}

func cloneCommandSpec(spec CommandSpec) CommandSpec {
	return CommandSpec{
		Path: spec.Path, Args: append([]string(nil), spec.Args...),
		Workdir: spec.Workdir, SessionID: spec.SessionID, executableInfo: spec.executableInfo, mode: spec.mode,
	}
}

func sameExecutable(spec CommandSpec) bool {
	current, err := os.Stat(spec.Path)
	return err == nil && secureClaudeExecutable(current) && os.SameFile(current, spec.executableInfo)
}

func secureClaudeExecutable(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0 && info.Mode().Perm()&0o022 == 0
}

// ExecutableMatches reports whether the pinned command is still the exact
// regular file selected by BuildCommandSpec. OS factories must call it
// immediately before starting the process.
func (spec CommandSpec) ExecutableMatches() bool {
	return validBuiltCommand(spec) && sameExecutable(spec)
}
