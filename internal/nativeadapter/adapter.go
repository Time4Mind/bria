// Package nativeadapter bridges one owned native CLI terminal to Bria's
// correlated runtime protocol. Terminal controls never become model turns.
package nativeadapter

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/nativecapture"
	"bria/internal/nativecli"
	"bria/internal/nativeterminal"
	"bria/internal/nativetranscript"
	"bria/internal/runtimeprotocol"
)

type Config struct {
	Persistent, AttachOnly      bool
	LogicalSessionID            string
	Provider                    domain.Provider
	Command                     []string
	Workdir, ResumeID, StateDir string
	Environment                 []string
	ScreenCaptureLimitKiB       int
}
type activeInput struct {
	request   runtimeprotocol.ParentMessage
	text      string
	accepted  bool
	turnID    string
	completed bool
	sent      time.Time
}
type adapter struct {
	mediaDir    string
	config      Config
	term        *nativeterminal.Terminal
	output      io.Writer
	id, model   string
	reader      *nativetranscript.Reader
	active      *activeInput
	steers      []*activeInput
	final       string
	receipts    map[string]string
	turnIDs     map[string]string
	observation nativeObservationState
}

func Run(ctx context.Context, input io.ReadCloser, output io.Writer, config Config) (returnErr error) {
	originalInput := input
	defer originalInput.Close()
	var inputErr error
	input, inputErr = interruptibleInput(input)
	if inputErr != nil {
		return inputErr
	}
	defer input.Close()
	if config.AttachOnly && (!config.Persistent || config.ResumeID == "") ||
		config.Persistent && (config.LogicalSessionID == "" || !filepath.IsAbs(config.StateDir)) {
		return nativeterminal.ErrBindingMismatch
	}
	environment := childEnvironment(config.Environment)
	binding := nativeterminal.Binding{LogicalSessionID: config.LogicalSessionID, NativeSessionID: config.ResumeID, Provider: string(config.Provider), Workdir: config.Workdir}
	var term *nativeterminal.Terminal
	var plan nativecli.Plan
	var err error
	if config.Persistent && config.ResumeID != "" {
		term, err = nativeterminal.AttachExisting(ctx, config.StateDir, binding)
		if err != nil && (config.AttachOnly || !errors.Is(err, nativeterminal.ErrNotManaged)) {
			return err
		}
	}
	attached := term != nil
	if !attached {
		plan, err = nativecli.BuildWithPolicy(config.Provider, config.Command, config.Workdir, config.ResumeID, nativeLaunchPolicy(os.Geteuid(), environment))
		if err == nil {
			term, err = nativeterminal.Open(ctx, nativeterminal.Config{Persistent: config.Persistent, Command: plan.Command, Workdir: config.Workdir, Environment: environment, LiteralInputBarrier: config.Provider == domain.ProviderCodex})
		}
	}
	if err != nil {
		return err
	}
	bound, physicallyClosed := attached, false
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if bound && !physicallyClosed {
			returnErr = errors.Join(returnErr, term.Detach(closeCtx))
		} else {
			returnErr = errors.Join(returnErr, term.Close(closeCtx))
		}
	}()
	state := nativecli.State{SessionID: config.ResumeID}
	if !attached {
		startup, cancel := context.WithTimeout(ctx, 25*time.Second)
		state, err = nativecli.Ready(startup, term, plan)
		cancel()
		if err != nil {
			return err
		}
		if config.Persistent {
			if err := os.MkdirAll(config.StateDir, 0700); err != nil {
				return err
			}
			binding.NativeSessionID = state.SessionID
			if err := term.PersistBinding(ctx, config.StateDir, binding); err != nil {
				return err
			}
			bound = true
		}
	}
	a := &adapter{config: config, term: term, output: output, id: state.SessionID, model: state.Model, receipts: map[string]string{}}
	a.restoreMediaDirectory()
	if a.config.ScreenCaptureLimitKiB == 0 {
		a.config.ScreenCaptureLimitKiB = nativecapture.DefaultLimitKiB
	}
	defer func() {
		if !bound || physicallyClosed {
			returnErr = errors.Join(returnErr, a.cleanupAttachments())
		}
	}()
	if err = a.loadReceipts(); err != nil {
		return err
	}
	defer func() {
		if a.reader != nil {
			_ = a.reader.Close()
		}
	}()
	// Baseline already existing history, never replay it as a new accepted input.
	if !attached {
		if err = a.baseline(ctx); err != nil && !(errors.Is(err, nativetranscript.ErrNotFound) && a.reader == nil) {
			return err
		}
	}
	if err = a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeReady, ProviderSessionID: a.id, Readiness: "protocol", Authentication: "unknown", Model: a.model}); err != nil {
		return err
	}
	requests := make(chan runtimeprotocol.ParentMessage)
	readErr := make(chan error, 1)
	readCtx, stopRead := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			r, e := runtimeprotocol.DecodeParentLine(scanner.Bytes(), runtimeprotocol.Limits{})
			if e != nil {
				readErr <- e
				return
			}
			select {
			case requests <- r:
			case <-readCtx.Done():
				return
			}
			if r.Type == runtimeprotocol.TypeClose {
				return
			}
		}
		readErr <- scanner.Err()
	}()
	defer func() { stopRead(); _ = input.Close(); <-done }()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case r := <-requests:
			if r.Type == runtimeprotocol.TypeDetach && bound {
				return nil
			}
			if r.Type == runtimeprotocol.TypeClose {
				if bound {
					if err := term.Close(ctx); err != nil {
						return err
					}
					physicallyClosed = true
					return a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeClosed, ProviderSessionID: a.id})
				}
				return nil
			}
			if err = a.handle(ctx, r); err != nil {
				return err
			}
		case <-tick.C:
			alive, e := term.Alive(ctx)
			if e != nil {
				return e
			}
			if !alive {
				return errors.New("native CLI exited")
			}
			if a.active != nil {
				if err = a.poll(ctx); err != nil {
					return err
				}
			}
			if err = a.observeScreen(ctx, time.Now()); err != nil {
				return err
			}
		}
	}
}

func (a *adapter) emit(m runtimeprotocol.AdapterMessage) error {
	m.Protocol = 1
	b, e := runtimeprotocol.EncodeAdapterLine(m, runtimeprotocol.Limits{})
	if e != nil {
		return e
	}
	_, e = a.output.Write(b)
	return e
}
func (a *adapter) handle(ctx context.Context, r runtimeprotocol.ParentMessage) error {
	switch r.Type {
	case runtimeprotocol.TypeObserveAccepted:
		return a.observeAccepted(ctx, r)
	case runtimeprotocol.TypeNativeControl:
		return a.control(ctx, r)
	case runtimeprotocol.TypeReconcileAcceptedTurns:
		for id, status := range a.receipts {
			if err := a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeAcceptedTurn, RequestID: r.RequestID, MessageID: id, Status: status}); err != nil {
				return err
			}
		}
		return a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeReconciliationCompleted, RequestID: r.RequestID})
	case runtimeprotocol.TypeSubmit, runtimeprotocol.TypeSteer:
		if r.Type == runtimeprotocol.TypeSubmit && a.active != nil || r.Type == runtimeprotocol.TypeSteer && a.active == nil {
			return runtimeprotocol.ErrProtocol
		}
		text, err := a.inputText(ctx, r)
		if err != nil {
			return err
		}
		if r.Model != "" || r.Effort != "" {
			return errors.New("native model selection belongs to CLI")
		}
		if r.Type == runtimeprotocol.TypeSubmit {
			if err := a.baseline(ctx); err != nil && !(errors.Is(err, nativetranscript.ErrNotFound) && a.reader == nil) {
				return err
			}
		}
		pending := &activeInput{request: r, text: text, sent: time.Now()}
		if err := a.term.Input(ctx, text); err != nil {
			return err
		}
		if r.Type == runtimeprotocol.TypeSubmit {
			a.active = pending
			a.final = ""
		} else {
			a.steers = append(a.steers, pending)
		}
		return nil
	case runtimeprotocol.TypeInterrupt:
		if a.active == nil {
			return runtimeprotocol.ErrProtocol
		}
		return a.term.Key(ctx, "CtrlC")
	default:
		return runtimeprotocol.ErrProtocol
	}
}

func (a *adapter) control(ctx context.Context, r runtimeprotocol.ParentMessage) error {
	before, err := a.term.Capture(ctx)
	if err != nil {
		return err
	}
	hash := nativecapture.Hash(before)
	code := ""
	if fields := strings.Fields(r.Command); len(fields) > 0 {
		switch fields[0] {
		case "/new", "/clear", "/resume", "/fork":
			text := "Смена сессии выполняется через кнопки Bria: «Новая» или «Архив». Команда не отправлена: текущая привязка CLI сохранена."
			return a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeNativeSnapshot, RequestID: r.RequestID, Text: text, Hash: nativecapture.Hash(text), Model: a.model, ErrorCode: "unavailable"})
		}
	}
	if r.ExpectedHash != "" && r.ExpectedHash != hash {
		code = "stale"
	} else {
		if r.Command != "" {
			err = a.term.Input(ctx, r.Command)
		}
		if r.Key != "" {
			if r.Key == "approve_once" {
				err = nativecli.ApproveCommand(ctx, a.term, before)
				if err != nil {
					code = "unavailable"
					if errors.Is(err, nativecli.ErrApprovalStale) {
						code = "stale"
					}
					err = nil
				}
			} else {
				keys := map[string]string{"up": "Up", "down": "Down", "left": "Left", "right": "Right", "enter": "Enter", "escape": "Escape", "tab": "Tab", "space": "Space"}
				err = a.term.Key(ctx, keys[r.Key])
			}
		}
		if err != nil {
			return err
		}
	}
	text := before
	if code == "" && (r.Command != "" || r.Key != "") {
		// A bounded render turn, not a retry of the input side effect.
		for i := 0; i < 8; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(40 * time.Millisecond):
			}
			text, err = a.term.Capture(ctx)
			if err != nil {
				return err
			}
			if text != before {
				break
			}
		}
	}
	parsed := nativecli.ParseScreen(text)
	fullText := nativecapture.Bound(text, a.config.ScreenCaptureLimitKiB)
	rawHash := nativecapture.Hash(fullText)
	if parsed.Interactive {
		text = parsed.Content
	}
	if parsed.Model != "" {
		a.model = parsed.Model
	}
	maxBytes := a.config.ScreenCaptureLimitKiB * 1024
	if maxBytes <= 0 {
		maxBytes = nativecapture.DefaultLimitKiB * 1024
	}
	if len(text) > maxBytes {
		text = text[len(text)-maxBytes:]
		for !utf8.ValidString(text) {
			text = text[1:]
		}
	}
	return a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeNativeSnapshot, RequestID: r.RequestID, Text: text, FullText: fullText, Hash: rawHash, Model: a.model, Interactive: parsed.Interactive, ErrorCode: code})
}
func childEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "CCBOT_") || strings.HasPrefix(key, "BRIA_") || key == "CODEX_THREAD_ID" || key == "CODEX_SESSION_ID" || key == "CLAUDECODE" || key == "CLAUDE_CODE_ENTRYPOINT" {
			continue
		}
		result = append(result, value)
	}
	return result
}

func (a *adapter) openReader(ctx context.Context) error {
	if a.reader != nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".codex", "sessions")
	if a.config.Provider == domain.ProviderClaude {
		root = filepath.Join(home, ".claude", "projects")
	}
	if a.config.Provider == domain.ProviderCodex && os.Getenv("CODEX_HOME") != "" {
		root = filepath.Join(os.Getenv("CODEX_HOME"), "sessions")
	}
	if a.config.Provider == domain.ProviderClaude && os.Getenv("CLAUDE_CONFIG_DIR") != "" {
		root = filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects")
	}
	a.reader, err = nativetranscript.Open(ctx, nativetranscript.Options{Provider: string(a.config.Provider), SessionID: a.id, Workdir: a.config.Workdir, Root: root})
	return err
}
func (a *adapter) baseline(ctx context.Context) error {
	if err := a.openReader(ctx); err != nil {
		return err
	}
	return a.reader.Drain(ctx)
}
func (a *adapter) poll(ctx context.Context) error {
	if err := a.openReader(ctx); err != nil {
		if errors.Is(err, nativetranscript.ErrNotFound) && time.Since(a.active.sent) < 20*time.Second {
			return nil
		}
		return err
	}
	events, err := a.reader.Poll(ctx)
	if err != nil {
		return err
	}
	if err := a.consumeEvents(events); err != nil {
		return err
	}
	for _, p := range append([]*activeInput{a.active}, a.steers...) {
		if p != nil && !p.accepted && time.Since(p.sent) > 20*time.Second {
			return errors.New("native provider acceptance not confirmed")
		}
	}
	return nil
}

func (a *adapter) consumeEvents(events []nativetranscript.Event) error {
	var err error
	for _, event := range events {
		if event.Model != "" {
			a.model = event.Model
		}
		if a.active == nil {
			break
		}
		switch event.Kind {
		case nativetranscript.KindUser:
			pending := append([]*activeInput{a.active}, a.steers...)
			for _, p := range pending {
				if !p.accepted && strings.TrimSpace(event.Text) == strings.TrimSpace(p.text) {
					if event.TurnID == "" {
						return errors.New("native input turn identity missing")
					}
					p.accepted = true
					p.turnID = event.TurnID
					if err = a.acceptReceipt(p.request.MessageID, event.TurnID); err != nil {
						return err
					}
					if p.request.MessageID != "" {
						if err = a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeAccepted, RequestID: p.request.RequestID, MessageID: p.request.MessageID}); err != nil {
							return err
						}
					}
					break
				}
			}
		case nativetranscript.KindCommentary, nativetranscript.KindTool, nativetranscript.KindQuestion, nativetranscript.KindThinking:
			if a.matchesPendingTurn(event.TurnID) && event.Text != "" {
				text := event.Text
				kind := "commentary"
				if event.Kind == nativetranscript.KindQuestion {
					kind = "question"
				}
				if event.Kind == nativetranscript.KindTool {
					kind = "tool"
				}
				if event.Kind == nativetranscript.KindThinking {
					kind = "thinking"
				}
				if err = a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeEvent, RequestID: a.active.request.RequestID, Kind: kind, Text: text, EventMetadata: event.Metadata, EventID: event.ID}); err != nil {
					return err
				}
			}
		case nativetranscript.KindFinal:
			if a.matchesPendingTurn(event.TurnID) {
				a.final = event.Text
			}
		case nativetranscript.KindComplete:
			if a.matchesPendingTurn(event.TurnID) {
				if err = a.complete(event.TurnID); err != nil {
					return err
				}
			}
		case nativetranscript.KindInterrupted:
			if a.matchesPendingTurn(event.TurnID) {
				id := a.active.request.RequestID
				for _, p := range append([]*activeInput{a.active}, a.steers...) {
					if p.accepted && !p.completed && p.turnID == event.TurnID {
						a.receipts[p.request.MessageID] = "failed"
					}
				}
				if err = a.saveReceipts(); err != nil {
					return err
				}
				if err = a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeCompleted, RequestID: id, Status: "interrupted", ErrorCode: "interrupted"}); err != nil {
					return err
				}
				a.active = nil
				a.steers = nil
				a.final = ""
			}
		}
	}
	return nil
}

func (a *adapter) matchesPendingTurn(turnID string) bool {
	if turnID == "" || a.active == nil || !a.active.accepted {
		return false
	}
	for _, p := range append([]*activeInput{a.active}, a.steers...) {
		if p != nil && p.accepted && !p.completed && p.turnID == turnID {
			return true
		}
	}
	return false
}

func (a *adapter) complete(turnID string) error {
	id := a.active.request.RequestID
	allCompleted := true
	for _, p := range append([]*activeInput{a.active}, a.steers...) {
		if p.accepted && p.turnID == turnID {
			p.completed = true
			a.receipts[p.request.MessageID] = "completed"
		}
		allCompleted = allCompleted && p.completed
	}
	if err := a.saveReceipts(); err != nil {
		return err
	}
	if !allCompleted {
		return nil
	}
	if a.final != "" {
		if err := a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeFinal, RequestID: id, Text: a.final}); err != nil {
			return err
		}
	}
	title := ""
	if a.reader != nil {
		title, _ = a.reader.Title(context.Background())
	}
	if err := a.emit(runtimeprotocol.AdapterMessage{Type: runtimeprotocol.TypeCompleted, RequestID: id, Status: "completed", ProviderSessionName: title}); err != nil {
		return err
	}
	a.active = nil
	a.steers = nil
	a.final = ""
	return nil
}

func Main(ctx context.Context, provider domain.Provider, args []string, input io.ReadCloser, output io.Writer) error {
	if len(args) < 2 || args[0] != "--" {
		return errors.New("native adapter raw command required")
	}
	workdir, err := os.Getwd()
	if err != nil {
		return err
	}
	mode := os.Getenv("BRIA_START_MODE")
	resume := os.Getenv("BRIA_PROVIDER_SESSION_ID")
	if mode != "new" && mode != "resume" || mode == "new" && resume != "" || mode == "resume" && resume == "" {
		return fmt.Errorf("native start identity invalid")
	}
	limit := nativecapture.DefaultLimitKiB
	if raw := os.Getenv("BRIA_SCREEN_CAPTURE_KIB"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil {
			limit = parsed
		}
	}
	return Run(ctx, input, output, Config{Provider: provider, Command: args[1:], Workdir: workdir, ResumeID: resume,
		StateDir: os.Getenv("BRIA_NATIVE_STATE_DIR"), Environment: os.Environ(), ScreenCaptureLimitKiB: limit,
		Persistent: os.Getenv("BRIA_PERSISTENT_TERMINAL") == "1", AttachOnly: os.Getenv("BRIA_ATTACH_ONLY") == "1", LogicalSessionID: os.Getenv("BRIA_SESSION_ID")})
}
