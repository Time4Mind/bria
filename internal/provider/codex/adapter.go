package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"bria/internal/processgroup"
	"bria/internal/runtimeprotocol"
)

const (
	AdapterProtocolVersion       = 1
	DefaultMaxParentMessageBytes = 64 * 1024
	DefaultMaxAdapterTextBytes   = 32 * 1024
	DefaultMaxQueuedSubmits      = 32
	stderrDrainBufferBytes       = 32 * 1024
	statusCompleted              = "completed"
	statusFailed                 = "failed"
	statusInterrupted            = "interrupted"
	errorCodeProvider            = "provider_error"
	errorCodeProtocol            = "protocol_error"
	errorCodeTransport           = "transport_error"
	errorCodeInterrupted         = "interrupted"
)

var (
	ErrAdapterConfiguration = errors.New("invalid codex adapter configuration")
	ErrAdapterProtocol      = errors.New("codex adapter protocol violation")
	ErrAdapterTransport     = errors.New("codex adapter transport failed")
	ErrRawProcess           = errors.New("raw codex app-server process failed")
)

type AdapterConfig struct {
	// RawCommand is executed directly. Its first element must be an absolute
	// executable and its argv must select app-server stdio mode.
	RawCommand []string
	RawEnv     []string
	Workdir    string
	// ResumeThreadID is the exact persisted Codex thread.id to resume. Empty
	// means create a new thread.
	ResumeThreadID        string
	MaxParentMessageBytes int
	MaxTextBytes          int
	MaxQueuedSubmits      int
	ClientInfo            ClientInfo
}

// RunAdapter owns one raw Codex app-server child until a close request,
// cancellation, or error. It never invokes a shell and confirms child exit on
// every return path after a successful start.
func RunAdapter(ctx context.Context, parentInput io.ReadCloser, parentOutput io.Writer, config AdapterConfig) error {
	if err := validateAdapterConfig(parentInput, parentOutput, config); err != nil {
		return err
	}
	resolvedExecutable, err := resolveRawExecutable(config.RawCommand[0])
	if err != nil {
		return err
	}
	config.RawCommand = append([]string(nil), config.RawCommand...)
	config.RawCommand[0] = resolvedExecutable
	maxParentBytes := config.MaxParentMessageBytes
	if maxParentBytes == 0 {
		maxParentBytes = DefaultMaxParentMessageBytes
	}
	maxTextBytes := config.MaxTextBytes
	if maxTextBytes == 0 {
		maxTextBytes = DefaultMaxAdapterTextBytes
	}
	maxQueuedSubmits := config.MaxQueuedSubmits
	if maxQueuedSubmits == 0 {
		maxQueuedSubmits = DefaultMaxQueuedSubmits
	}

	process, err := startRawProcess(config)
	if err != nil {
		return err
	}
	defer process.stop()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session := &adapterSession{
		output:         parentOutput,
		maxOutputBytes: maxParentBytes,
		maxTextBytes:   maxTextBytes,
		turnResults:    make(chan error, 1),
	}
	defer func() {
		session.markClosing()
		cancel()
		process.stop()
		session.waitForTurn()
	}()
	client, err := NewClient(process.stdout, process.stdin, Options{
		ClientInfo:  config.ClientInfo,
		InputCloser: process.stdout,
		OnNotification: func(notification Notification) error {
			return session.handleNotification(notification)
		},
		OnServerRequest: func(requestCtx context.Context, request ServerRequest) (ServerResponse, error) {
			return session.handleServerRequest(requestCtx, request)
		},
		OnServerResponseAccepted: func(request ServerRequest) error {
			return session.writeInteractionResponseAccepted(request)
		},
	})
	if err != nil {
		return ErrAdapterConfiguration
	}
	session.client = client

	if _, err := client.Initialize(runCtx); err != nil {
		return classifyStartupError(err)
	}
	thread, err := client.StartThread(runCtx, ThreadStartRequest{
		Cwd:            config.Workdir,
		ResumeThreadID: config.ResumeThreadID,
	})
	if err != nil {
		return classifyStartupError(err)
	}
	session.threadID = thread.ThreadID
	session.providerSessionID = thread.ThreadID
	session.effectiveApprovalPolicy = thread.EffectiveApprovalPolicy
	session.effectiveSandbox = thread.EffectiveSandbox
	if err := session.writeReady(); err != nil {
		return err
	}

	requests := make(chan adapterRequest)
	readerDone := make(chan error, 1)
	stopReader := make(chan struct{})
	go readAdapterRequests(parentInput, maxParentBytes, requests, readerDone, stopReader, session.latchDecodedRequest)
	defer func() {
		close(stopReader)
		_ = parentInput.Close()
	}()
	var queuedSubmits []adapterRequest
	turnRunning := false

	for {
		select {
		case request := <-requests:
			switch request.Type {
			case "submit":
				if turnRunning {
					if len(queuedSubmits) >= maxQueuedSubmits {
						return ErrAdapterProtocol
					}
					queuedSubmits = append(queuedSubmits, request)
					continue
				}
				if !session.beginTurn(request.RequestID, request.MessageID) {
					return ErrAdapterProtocol
				}
				turnRunning = true
				go session.runTurn(runCtx, request)
			case "interrupt":
				if err := session.interrupt(runCtx, request.RequestID); err != nil {
					return err
				}
			case "interaction_response":
				if request.InteractionResponse == nil || !session.resolveInteraction(request.RequestID, *request.InteractionResponse) {
					return ErrAdapterProtocol
				}
			case "reconcile_accepted_turns":
				if turnRunning || len(queuedSubmits) != 0 {
					return ErrAdapterProtocol
				}
				if err := session.writeAcceptedTurnReconciliation(runCtx, request.RequestID); err != nil {
					return err
				}
			case "close":
				session.markClosing()
				cancel()
				process.stop()
				session.waitForTurn()
				return nil
			default:
				return ErrAdapterProtocol
			}
		case turnError := <-session.turnResults:
			turnRunning = false
			if turnError != nil {
				return turnError
			}
			if len(queuedSubmits) > 0 {
				next := queuedSubmits[0]
				queuedSubmits = queuedSubmits[1:]
				if !session.beginTurn(next.RequestID, next.MessageID) {
					return ErrAdapterProtocol
				}
				turnRunning = true
				go session.runTurn(runCtx, next)
			}
		case readErr := <-readerDone:
			return readErr
		case <-process.done:
			session.markClosing()
			cancel()
			session.waitForTurn()
			return ErrRawProcess
		case <-ctx.Done():
			session.markClosing()
			cancel()
			process.stop()
			session.waitForTurn()
			return ctx.Err()
		}
	}
}

func validateAdapterConfig(parentInput io.ReadCloser, parentOutput io.Writer, config AdapterConfig) error {
	if parentInput == nil || parentOutput == nil || len(config.RawCommand) < 2 ||
		!filepath.IsAbs(config.RawCommand[0]) || !filepath.IsAbs(config.Workdir) ||
		config.ClientInfo.Name == "" || config.ClientInfo.Version == "" ||
		config.MaxParentMessageBytes < 0 || config.MaxTextBytes < 0 || config.MaxQueuedSubmits < 0 ||
		(config.ResumeThreadID != "" && !validRequestID(config.ResumeThreadID)) {
		return ErrAdapterConfiguration
	}
	appServerCount := 0
	for _, argument := range config.RawCommand[1:] {
		lower := strings.ToLower(argument)
		if argument == "app-server" {
			appServerCount++
		}
		if lower == "daemon" || lower == "proxy" || strings.HasPrefix(lower, "--listen") ||
			strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
			strings.Contains(lower, "auth") {
			return ErrAdapterConfiguration
		}
	}
	if appServerCount != 1 || config.RawCommand[len(config.RawCommand)-1] != "app-server" {
		return ErrAdapterConfiguration
	}
	return nil
}

type rawProcess struct {
	command     *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	stderr      io.ReadCloser
	outputEOF   chan struct{}
	done        chan struct{}
	stderrDone  chan struct{}
	sharedGroup bool
	lifecycleMu sync.Mutex
	reaping     bool
	stopOnce    sync.Once
}

type eofReadCloser struct {
	io.ReadCloser
	eof  chan struct{}
	once sync.Once
}

func (reader *eofReadCloser) Read(buffer []byte) (int, error) {
	read, err := reader.ReadCloser.Read(buffer)
	if err != nil {
		reader.once.Do(func() { close(reader.eof) })
	}
	return read, err
}

func (reader *eofReadCloser) Close() error {
	reader.once.Do(func() { close(reader.eof) })
	return reader.ReadCloser.Close()
}

func startRawProcess(config AdapterConfig) (*rawProcess, error) {
	command := exec.Command(config.RawCommand[0], config.RawCommand[1:]...)
	command.Dir = config.Workdir
	if config.RawEnv != nil {
		command.Env = append([]string(nil), config.RawEnv...)
	}
	sharedGroup, err := processgroup.ConfigureDescendant(command)
	if err != nil {
		return nil, ErrRawProcess
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, ErrRawProcess
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, ErrRawProcess
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, ErrRawProcess
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, ErrRawProcess
	}
	outputEOF := make(chan struct{})
	trackedStdout := &eofReadCloser{ReadCloser: stdout, eof: outputEOF}
	process := &rawProcess{
		command:     command,
		stdin:       stdin,
		stdout:      trackedStdout,
		stderr:      stderr,
		outputEOF:   outputEOF,
		done:        make(chan struct{}),
		stderrDone:  make(chan struct{}),
		sharedGroup: sharedGroup,
	}
	go func() {
		buffer := make([]byte, stderrDrainBufferBytes)
		_, _ = io.CopyBuffer(io.Discard, stderr, buffer)
		close(process.stderrDone)
	}()
	go process.reapOnOutputEnd()
	return process, nil
}

func (process *rawProcess) stop() {
	process.stopOnce.Do(func() {
		process.lifecycleMu.Lock()
		if process.reaping {
			process.lifecycleMu.Unlock()
			<-process.done
			return
		}
		process.reaping = true
		if process.sharedGroup {
			// The adapter is the verified leader of the group inherited by raw
			// Codex and all descendants. Killing the current tree is deliberately
			// non-returning and lets an outer runtime Wait confirm complete exit.
			_ = processgroup.KillCurrentTree()
			return
		}
		// The signal is serialized before the sole Wait, so neither Cmd state nor
		// a re-used PID can race with group termination.
		if err := processgroup.KillTree(process.command); err != nil {
			_ = process.command.Process.Kill()
		}
		process.waitAndClose()
		process.lifecycleMu.Unlock()
	})
}

func (process *rawProcess) reapOnOutputEnd() {
	<-process.outputEOF
	process.lifecycleMu.Lock()
	if process.reaping {
		process.lifecycleMu.Unlock()
		return
	}
	process.reaping = true
	if process.sharedGroup {
		_ = processgroup.KillCurrentTree()
		return
	}
	// stdout ended while the leader is deliberately still unreaped. Clear its
	// complete standalone group before the one and only Wait.
	_ = processgroup.KillTree(process.command)
	process.waitAndClose()
	process.lifecycleMu.Unlock()
}

func (process *rawProcess) waitAndClose() {
	_ = process.command.Wait()
	_ = process.stdin.Close()
	_ = process.stdout.Close()
	_ = process.stderr.Close()
	<-process.stderrDone
	close(process.done)
}

type adapterRequest struct {
	Protocol            int
	Type                string
	RequestID           string
	MessageID           string
	Text                *string
	Attachments         []runtimeprotocol.LocalAttachment
	InteractionResponse *runtimeprotocol.InteractionResponse
}

func readAdapterRequests(
	input io.Reader,
	maxBytes int,
	requests chan<- adapterRequest,
	done chan<- error,
	stop <-chan struct{},
	onDecoded func(adapterRequest),
) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, min(maxBytes, 4096)), maxBytes)
	for scanner.Scan() {
		request, err := decodeAdapterRequest(scanner.Bytes())
		if err != nil {
			done <- err
			return
		}
		if onDecoded != nil {
			onDecoded(request)
		}
		select {
		case requests <- request:
		case <-stop:
			done <- nil
			return
		}
	}
	select {
	case <-stop:
		done <- nil
	default:
		done <- ErrAdapterTransport
	}
}

func decodeAdapterRequest(line []byte) (adapterRequest, error) {
	decoded, err := runtimeprotocol.DecodeParentLine(line, runtimeprotocol.Limits{})
	if err != nil {
		return adapterRequest{}, ErrAdapterProtocol
	}
	request := adapterRequest{Protocol: decoded.Protocol, Type: string(decoded.Type), RequestID: decoded.RequestID, MessageID: decoded.MessageID, Attachments: append([]runtimeprotocol.LocalAttachment(nil), decoded.Attachments...), InteractionResponse: decoded.InteractionResponse}
	if decoded.Type == runtimeprotocol.TypeSubmit {
		request.Text = &decoded.Text
	}
	return request, nil
}

func hasDuplicateObjectField(line []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(line))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return true
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return true
		}
		name, ok := key.(string)
		if !ok {
			return true
		}
		if _, duplicate := seen[name]; duplicate {
			return true
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return true
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return true
	}
	var trailing any
	return !errors.Is(decoder.Decode(&trailing), io.EOF)
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

type activeAdapterTurn struct {
	requestID          string
	messageID          string
	turnID             string
	accepted           bool
	interruptRequested bool
	interruptSent      bool
	terminalCommitted  bool
}

type adapterSession struct {
	client                  *Client
	threadID                string
	providerSessionID       string
	effectiveApprovalPolicy string
	effectiveSandbox        Sandbox
	output                  io.Writer
	maxOutputBytes          int
	maxTextBytes            int
	outputMu                sync.Mutex
	mu                      sync.Mutex
	active                  *activeAdapterTurn
	pendingInterrupts       map[string]struct{}
	closing                 bool
	turnResults             chan error
	turnWG                  sync.WaitGroup
	interaction             *pendingAdapterInteraction
}

type pendingAdapterInteraction struct {
	requestID string
	request   runtimeprotocol.InteractionRequest
	response  chan runtimeprotocol.InteractionResponse
}

func (session *adapterSession) beginTurn(requestID, messageID string) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closing || session.active != nil {
		return false
	}
	session.active = &activeAdapterTurn{
		requestID: requestID, messageID: messageID,
	}
	_, session.active.interruptRequested = session.pendingInterrupts[requestID]
	session.turnWG.Add(1)
	return true
}

func (session *adapterSession) runTurn(ctx context.Context, request adapterRequest) {
	defer session.turnWG.Done()
	var turnResult error
	defer func() { session.turnResults <- turnResult }()
	images, verifyErr := verifiedLocalImages(ctx, request.Attachments)
	if verifyErr != nil {
		session.finishTurn(request.RequestID)
		turnResult = ErrAdapterProtocol
		return
	}
	var textInput []TextInput
	if *request.Text != "" {
		textInput = []TextInput{{Text: *request.Text}}
	}
	outcome, err := session.client.StartTurn(ctx, TurnStartRequest{
		ThreadID:    session.threadID,
		MessageID:   request.MessageID,
		Input:       textInput,
		LocalImages: images,
		OnAccepted: func(accepted TurnAccepted) error {
			session.mu.Lock()
			if session.closing || session.active == nil || session.active.requestID != request.RequestID {
				session.mu.Unlock()
				return context.Canceled
			}
			session.active.turnID = accepted.TurnID
			session.mu.Unlock()
			if err := session.writeAccepted(request.RequestID, request.MessageID); err != nil {
				return err
			}
			session.mu.Lock()
			if session.active != nil && session.active.requestID == request.RequestID {
				session.active.accepted = true
			}
			session.mu.Unlock()
			return nil
		},
		InterruptAfterAccepted: func(TurnAccepted) bool {
			return session.claimPendingInterrupt(request.RequestID)
		},
	})

	closing, accepted, cancellationWon := session.commitTerminalPublication(request.RequestID)
	if closing {
		session.finishTurn(request.RequestID)
		return
	}
	if !accepted {
		session.finishTurn(request.RequestID)
		turnResult = classifyStartupError(err)
		return
	}

	var outputError error
	if cancellationWon {
		outputError = session.writeCompleted(request.RequestID, statusInterrupted, errorCodeInterrupted)
	} else if err == nil && !outcome.InterruptAcknowledged && outcome.Status == statusCompleted && len(outcome.Error) == 0 &&
		outcome.Final != nil && validAdapterText(outcome.Final.Text, session.maxTextBytes) {
		if outputError = session.writeFinal(request.RequestID, outcome.Final.Text); outputError == nil {
			outputError = session.writeCompleted(request.RequestID, statusCompleted, "")
		}
	} else {
		status, code := classifyTurnFailure(outcome, err)
		outputError = session.writeCompleted(request.RequestID, status, code)
	}
	session.finishTurn(request.RequestID)
	if outputError != nil {
		turnResult = outputError
	} else if fatal := fatalTurnError(err); fatal != nil {
		turnResult = fatal
	}
}

func verifiedLocalImages(ctx context.Context, attachments []runtimeprotocol.LocalAttachment) ([]LocalImageInput, error) {
	images := make([]LocalImageInput, 0, len(attachments))
	for _, attachment := range attachments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before, err := os.Lstat(attachment.Path)
		if err != nil || !before.Mode().IsRegular() || before.Size() != attachment.Size {
			return nil, ErrAdapterProtocol
		}
		file, err := os.Open(attachment.Path)
		if err != nil {
			return nil, ErrAdapterProtocol
		}
		after, statErr := file.Stat()
		hash := sha256.New()
		read, copyErr := io.Copy(hash, io.LimitReader(file, attachment.Size+1))
		final, finalErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || !os.SameFile(before, after) || copyErr != nil || read != attachment.Size ||
			finalErr != nil || !os.SameFile(before, final) || final.Size() != attachment.Size || closeErr != nil ||
			hex.EncodeToString(hash.Sum(nil)) != attachment.SHA256 {
			return nil, ErrAdapterProtocol
		}
		images = append(images, LocalImageInput{Path: attachment.Path})
	}
	return images, nil
}

func fatalTurnError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnsupportedRequest), errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrInvalidResponse),
		errors.Is(err, ErrMalformedMessage), errors.Is(err, ErrMessageTooLarge):
		return ErrAdapterProtocol
	case errors.Is(err, ErrTransport), errors.Is(err, ErrUnexpectedEOF),
		errors.Is(err, ErrNotificationHandler), errors.Is(err, ErrUncancellableInput):
		return ErrAdapterTransport
	default:
		var terminal *TurnTerminalError
		if errors.As(err, &terminal) {
			return nil
		}
		return ErrRawProcess
	}
}

func (session *adapterSession) commitTerminalPublication(requestID string) (closing, accepted, cancellationWon bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	closing = session.closing
	if session.active == nil || session.active.requestID != requestID {
		return closing, false, false
	}
	accepted = session.active.accepted
	cancellationWon = session.active.interruptRequested
	session.active.terminalCommitted = true
	return closing, accepted, cancellationWon
}

func (session *adapterSession) finishTurn(requestID string) {
	session.mu.Lock()
	if session.active != nil && session.active.requestID == requestID {
		session.active = nil
	}
	delete(session.pendingInterrupts, requestID)
	session.mu.Unlock()
}

func (session *adapterSession) latchDecodedRequest(request adapterRequest) {
	if request.Type != "interrupt" {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.pendingInterrupts == nil {
		session.pendingInterrupts = make(map[string]struct{})
	}
	session.pendingInterrupts[request.RequestID] = struct{}{}
	if session.active != nil && session.active.requestID == request.RequestID && !session.active.terminalCommitted {
		session.active.interruptRequested = true
	}
}

func (session *adapterSession) claimPendingInterrupt(requestID string) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closing || session.active == nil || session.active.requestID != requestID ||
		!session.active.interruptRequested || session.active.interruptSent {
		return false
	}
	session.active.interruptSent = true
	return true
}

func (session *adapterSession) interrupt(ctx context.Context, requestID string) error {
	session.mu.Lock()
	if session.closing || session.active == nil || session.active.requestID != requestID {
		session.mu.Unlock()
		return nil
	}
	if session.active.terminalCommitted {
		delete(session.pendingInterrupts, requestID)
		session.mu.Unlock()
		return nil
	}
	session.active.interruptRequested = true
	if session.interaction != nil && session.interaction.requestID == requestID {
		pending := session.interaction
		session.interaction = nil
		select {
		case pending.response <- runtimeprotocol.InteractionResponse{ID: pending.request.ID, Outcome: runtimeprotocol.OutcomeCancelled}:
		default:
		}
		if pending.request.Kind == runtimeprotocol.InteractionQuestion {
			// The request handler fails closed on a cancelled question and the
			// Codex client emits exactly one turn/interrupt itself. Sending another
			// request here would corrupt response correlation on the raw stream.
			session.mu.Unlock()
			return nil
		}
	}
	if session.active.turnID == "" || session.active.interruptSent {
		session.mu.Unlock()
		return nil
	}
	session.active.interruptSent = true
	turnID := session.active.turnID
	threadID := session.threadID
	session.mu.Unlock()
	if _, err := session.client.RequestInterrupt(ctx, threadID, turnID); err != nil {
		if errors.Is(err, ErrNoActiveTurn) || errors.Is(err, ErrActiveTurnMismatch) {
			return nil
		}
		return ErrAdapterTransport
	}
	return nil
}

func (session *adapterSession) markClosing() {
	session.mu.Lock()
	session.closing = true
	session.mu.Unlock()
}

func (session *adapterSession) waitForTurn() {
	session.turnWG.Wait()
}

func (session *adapterSession) handleNotification(notification Notification) error {
	if notification.Method != "item/completed" {
		return nil
	}
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Phase string `json:"phase"`
		} `json:"item"`
	}
	if json.Unmarshal(notification.Params, &params) != nil || params.Item.Type != "agentMessage" ||
		params.Item.Phase != "commentary" || !validAdapterText(params.Item.Text, session.maxTextBytes) {
		return nil
	}
	session.mu.Lock()
	if session.closing || session.active == nil || session.active.turnID == "" ||
		params.ThreadID != session.threadID || params.TurnID != session.active.turnID {
		session.mu.Unlock()
		return nil
	}
	requestID := session.active.requestID
	session.mu.Unlock()
	return session.writeEvent(requestID, "commentary", params.Item.Text)
}

func (session *adapterSession) handleServerRequest(ctx context.Context, request ServerRequest) (ServerResponse, error) {
	interaction, err := providerInteraction(request)
	if err != nil {
		return ServerResponse{}, err
	}
	session.mu.Lock()
	if session.closing || session.active == nil || session.active.turnID == "" || session.interaction != nil ||
		request.ThreadID != session.threadID || request.TurnID != session.active.turnID {
		session.mu.Unlock()
		return ServerResponse{}, ErrAdapterProtocol
	}
	requestID := session.active.requestID
	pending := &pendingAdapterInteraction{requestID: requestID, request: interaction, response: make(chan runtimeprotocol.InteractionResponse, 1)}
	session.interaction = pending
	session.mu.Unlock()

	if err := session.writeInteraction(requestID, interaction); err != nil {
		session.clearInteraction(pending)
		return ServerResponse{}, err
	}
	select {
	case response := <-pending.response:
		session.clearInteraction(pending)
		if response.Outcome == runtimeprotocol.OutcomeCancelled {
			if request.Kind == ServerRequestQuestion {
				return ServerResponse{}, context.Canceled
			}
			return ServerResponse{Decision: ApprovalCancel}, nil
		}
		return providerResponse(request, response)
	case <-ctx.Done():
		session.clearInteraction(pending)
		return ServerResponse{}, ctx.Err()
	}
}

func (session *adapterSession) clearInteraction(pending *pendingAdapterInteraction) {
	session.mu.Lock()
	if session.interaction == pending {
		session.interaction = nil
	}
	session.mu.Unlock()
}

func (session *adapterSession) resolveInteraction(requestID string, response runtimeprotocol.InteractionResponse) bool {
	session.mu.Lock()
	pending := session.interaction
	if pending == nil || pending.requestID != requestID || runtimeprotocol.ValidateResponse(pending.request, response, runtimeprotocol.Limits{}) != nil {
		session.mu.Unlock()
		return false
	}
	session.interaction = nil
	session.mu.Unlock()
	pending.response <- response
	return true
}

func providerInteraction(request ServerRequest) (runtimeprotocol.InteractionRequest, error) {
	result := runtimeprotocol.InteractionRequest{ID: request.InteractionID, ThreadID: request.ThreadID, TurnID: request.TurnID}
	switch request.Kind {
	case ServerRequestQuestion:
		if request.Question == nil {
			return runtimeprotocol.InteractionRequest{}, ErrAdapterProtocol
		}
		result.Kind = runtimeprotocol.InteractionQuestion
		result.ItemID = request.Question.ItemID
		result.Blocking = request.Question.IsBlocking
		result.Questions = make([]runtimeprotocol.Question, len(request.Question.Questions))
		for index, question := range request.Question.Questions {
			options := make([]runtimeprotocol.Option, len(question.Options))
			for optionIndex, option := range question.Options {
				options[optionIndex] = runtimeprotocol.Option{Label: option.Label, Description: option.Description}
			}
			result.Questions[index] = runtimeprotocol.Question{ID: question.ID, Header: question.Header, Text: question.Question, Options: options, IsOther: question.IsOther, IsSecret: question.IsSecret}
		}
	case ServerRequestCommandPermission:
		if request.Permission == nil {
			return runtimeprotocol.InteractionRequest{}, ErrAdapterProtocol
		}
		result.Kind = runtimeprotocol.InteractionCommandApproval
		result.ItemID = request.Permission.ItemID
		result.ApprovalID = request.Permission.ApprovalID
		result.StartedAtMS = request.Permission.StartedAtMS
		result.Reason = request.Permission.Reason
		result.Command = request.Permission.Command
		result.Cwd = request.Permission.Cwd
		result.Decisions = allApprovalDecisions()
	case ServerRequestFilePermission:
		if request.Permission == nil {
			return runtimeprotocol.InteractionRequest{}, ErrAdapterProtocol
		}
		result.Kind = runtimeprotocol.InteractionFileApproval
		result.ItemID = request.Permission.ItemID
		result.StartedAtMS = request.Permission.StartedAtMS
		result.Reason = request.Permission.Reason
		result.GrantRoot = request.Permission.GrantRoot
		result.Decisions = allApprovalDecisions()
	default:
		return runtimeprotocol.InteractionRequest{}, ErrAdapterProtocol
	}
	return result, nil
}

func allApprovalDecisions() []runtimeprotocol.ApprovalDecision {
	return []runtimeprotocol.ApprovalDecision{runtimeprotocol.DecisionAccept, runtimeprotocol.DecisionAcceptForSession, runtimeprotocol.DecisionDecline, runtimeprotocol.DecisionCancel}
}

func providerResponse(request ServerRequest, response runtimeprotocol.InteractionResponse) (ServerResponse, error) {
	if request.Kind == ServerRequestQuestion {
		return ServerResponse{Answers: response.Answers}, nil
	}
	return ServerResponse{Decision: ApprovalDecision(response.Decision)}, nil
}

func classifyStartupError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, ErrTransport), errors.Is(err, ErrUnexpectedEOF), errors.Is(err, ErrUncancellableInput):
		return ErrAdapterTransport
	default:
		return ErrRawProcess
	}
}

func classifyTurnFailure(outcome TurnOutcome, err error) (string, string) {
	if outcome.InterruptAcknowledged || outcome.Status == statusInterrupted {
		return statusInterrupted, errorCodeInterrupted
	}
	if errors.Is(err, ErrTransport) || errors.Is(err, ErrUnexpectedEOF) || errors.Is(err, ErrNotificationHandler) {
		return statusFailed, errorCodeTransport
	}
	if errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrInvalidResponse) || errors.Is(err, ErrMalformedMessage) ||
		errors.Is(err, ErrMessageTooLarge) || errors.Is(err, ErrUnsupportedRequest) {
		return statusFailed, errorCodeProtocol
	}
	return statusFailed, errorCodeProvider
}

type readyMessage struct {
	Protocol          int    `json:"protocol"`
	Type              string `json:"type"`
	ProviderSessionID string `json:"provider_session_id"`
	Readiness         string `json:"readiness"`
	Authentication    string `json:"authentication"`
}

type requestMessage struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Kind      string `json:"kind,omitempty"`
	Text      string `json:"text,omitempty"`
	Status    string `json:"status,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

func (session *adapterSession) writeReady() error {
	return session.write(readyMessage{
		Protocol:          AdapterProtocolVersion,
		Type:              "ready",
		ProviderSessionID: session.providerSessionID,
		Readiness:         "protocol",
		Authentication:    "unknown",
	})
}

func (session *adapterSession) writeAccepted(requestID, messageID string) error {
	return session.write(struct {
		Protocol  int    `json:"protocol"`
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		MessageID string `json:"message_id,omitempty"`
	}{AdapterProtocolVersion, "accepted", requestID, messageID})
}

func (session *adapterSession) writeEvent(requestID string, kind string, text string) error {
	return session.write(requestMessage{
		Protocol: AdapterProtocolVersion, Type: "event", RequestID: requestID, Kind: kind, Text: text,
	})
}

func (session *adapterSession) writeFinal(requestID string, text string) error {
	return session.write(requestMessage{Protocol: AdapterProtocolVersion, Type: "final", RequestID: requestID, Text: text})
}

func (session *adapterSession) writeCompleted(requestID string, status string, errorCode string) error {
	return session.write(requestMessage{
		Protocol: AdapterProtocolVersion, Type: "completed", RequestID: requestID, Status: status, ErrorCode: errorCode,
	})
}

func (session *adapterSession) writeInteraction(requestID string, interaction runtimeprotocol.InteractionRequest) error {
	encoded, err := runtimeprotocol.EncodeAdapterLine(runtimeprotocol.AdapterMessage{
		Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeInteractionRequest,
		RequestID: requestID, InteractionRequest: &interaction,
	}, runtimeprotocol.Limits{MaxLineBytes: session.maxOutputBytes, MaxTextBytes: session.maxTextBytes})
	if err != nil {
		return ErrAdapterProtocol
	}
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	written, err := session.output.Write(encoded)
	if err != nil || written != len(encoded) {
		return ErrAdapterTransport
	}
	return nil
}

func (session *adapterSession) writeInteractionResponseAccepted(request ServerRequest) error {
	session.mu.Lock()
	if session.closing || session.active == nil || session.active.requestID == "" || session.active.messageID == "" ||
		request.ThreadID != session.threadID || request.TurnID != session.active.turnID {
		session.mu.Unlock()
		return ErrAdapterProtocol
	}
	requestID := session.active.requestID
	messageID := session.active.messageID
	providerSessionID := session.providerSessionID
	session.mu.Unlock()
	encoded, err := runtimeprotocol.EncodeAdapterLine(runtimeprotocol.AdapterMessage{
		Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeInteractionResponseAccepted,
		ProviderSessionID: providerSessionID, RequestID: requestID, MessageID: messageID, InteractionID: request.InteractionID,
	}, runtimeprotocol.Limits{MaxLineBytes: session.maxOutputBytes, MaxTextBytes: session.maxTextBytes})
	if err != nil {
		return ErrAdapterProtocol
	}
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	written, err := session.output.Write(encoded)
	if err != nil || written != len(encoded) {
		return ErrAdapterTransport
	}
	return nil
}

func (session *adapterSession) writeAcceptedTurnReconciliation(ctx context.Context, requestID string) error {
	const pageSize = uint32(100)
	const maxPages = 100
	const maxTurns = 10_000
	cursor := ""
	seenCursors := make(map[string]struct{}, maxPages)
	seenMessages := make(map[string]struct{})
	receipts := make([]runtimeprotocol.AdapterMessage, 0, pageSize)
	turnCount := 0
	for pageNumber := 0; pageNumber < maxPages; pageNumber++ {
		page, err := session.client.ListThreadTurns(ctx, ThreadTurnsListRequest{ThreadID: session.threadID, Cursor: cursor, Limit: pageSize})
		if err != nil || len(page.Turns) > int(pageSize) || turnCount+len(page.Turns) > maxTurns {
			return ErrAdapterProtocol
		}
		turnCount += len(page.Turns)
		for _, turn := range page.Turns {
			status := "unknown"
			switch turn.Status {
			case ThreadTurnCompleted:
				status = "completed"
			case ThreadTurnFailed, ThreadTurnInterrupted:
				status = "failed"
			case ThreadTurnInProgress:
			default:
				return ErrAdapterProtocol
			}
			for _, messageID := range turn.MessageIDs {
				if _, duplicate := seenMessages[messageID]; duplicate {
					return ErrAdapterProtocol
				}
				seenMessages[messageID] = struct{}{}
				receipts = append(receipts, runtimeprotocol.AdapterMessage{
					Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeAcceptedTurn,
					RequestID: requestID, MessageID: messageID, Status: status,
				})
			}
		}
		if page.NextCursor == "" {
			for _, receipt := range receipts {
				if err := session.writeAdapterMessage(receipt); err != nil {
					return err
				}
			}
			return session.writeAdapterMessage(runtimeprotocol.AdapterMessage{
				Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeReconciliationCompleted, RequestID: requestID,
			})
		}
		if page.NextCursor == cursor {
			return ErrAdapterProtocol
		}
		if _, duplicate := seenCursors[page.NextCursor]; duplicate {
			return ErrAdapterProtocol
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return ErrAdapterProtocol
}

func (session *adapterSession) writeAdapterMessage(message runtimeprotocol.AdapterMessage) error {
	encoded, err := runtimeprotocol.EncodeAdapterLine(message, runtimeprotocol.Limits{
		MaxLineBytes: session.maxOutputBytes, MaxTextBytes: session.maxTextBytes,
	})
	if err != nil {
		return ErrAdapterProtocol
	}
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	written, err := session.output.Write(encoded)
	if err != nil || written != len(encoded) {
		return ErrAdapterTransport
	}
	return nil
}

func (session *adapterSession) write(message any) error {
	encoded, err := json.Marshal(message)
	if err != nil || len(encoded)+1 > session.maxOutputBytes {
		return ErrAdapterProtocol
	}
	encoded = append(encoded, '\n')
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	written, err := session.output.Write(encoded)
	if err != nil || written != len(encoded) {
		return ErrAdapterTransport
	}
	return nil
}

func validAdapterText(text string, maxBytes int) bool {
	return text != "" && utf8.ValidString(text) && len(text) <= maxBytes
}

func resolveRawExecutable(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolved) || unsafeRawExecutable(resolved) {
		return "", ErrAdapterConfiguration
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrAdapterConfiguration
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", ErrAdapterConfiguration
	}
	return resolved, nil
}

func unsafeRawExecutable(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "csh", "tcsh",
		"cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe",
		"env", "env.exe":
		return true
	default:
		return false
	}
}
