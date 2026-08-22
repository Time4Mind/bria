package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/interactive"
	"github.com/Time4Mind/bria/internal/promptidentity"
)

const inputConfirmationTimeout = 2 * time.Second
const inputConfirmationBaselineTimeout = 750 * time.Millisecond

func (e *LocalExecutor) executeOnce(
	ctx context.Context,
	request Request,
	binding RuntimeBinding,
) (Result, error) {
	result, _, err := e.executeOnceTimed(ctx, request, binding)
	return result, err
}

func (e *LocalExecutor) executeOnceTimed(
	ctx context.Context,
	request Request,
	binding RuntimeBinding,
) (Result, inputExecutionTiming, error) {
	timing := inputExecutionTiming{}
	current, err := e.resolveSession(request)
	if err != nil {
		return Result{Accepted: true, Detail: "runtime operation unavailable"}, timing, err
	}
	if current.snapshot() != binding {
		return Result{Accepted: true, Detail: "runtime target became stale"}, timing, ErrStaleRuntime
	}
	result := Result{Accepted: true, Detail: "runtime operation accepted"}
	err = nil
	switch request.Action {
	case ActionSendInput:
		text := request.Text
		if request.Input != nil {
			e.mu.RLock()
			resolver := e.inputs
			e.mu.RUnlock()
			if resolver == nil {
				err = ErrUnsupportedBackendAction
				timing.failureStage = "resolve"
				break
			}
			startedAt := e.now()
			if timed, ok := resolver.(TimedInputResolver); ok {
				var phase InputResolveTiming
				text, phase, err = timed.ResolveInputWithTiming(ctx, binding.Workdir, *request.Input)
				timing.download = phase.Download
				timing.transcribe = phase.Transcribe
			} else {
				text, err = resolver.ResolveInput(ctx, binding.Workdir, *request.Input)
			}
			timing.resolve = nonNegativeDuration(e.now().Sub(startedAt))
			if err != nil {
				timing.failureStage = "resolve"
				break
			}
			result.ResolvedText = text
		}
		timing.inputBytes = len([]byte(text))
		e.mu.RLock()
		confirmer := e.inputConfirmer
		e.mu.RUnlock()
		baseline := InputConfirmationBaseline{}
		baselineReady := false
		if confirmer != nil {
			baselineCtx, cancel := context.WithTimeout(ctx, inputConfirmationBaselineTimeout)
			baselineStartedAt := e.now()
			baseline, err = confirmer.BaselineInput(baselineCtx, binding)
			cancel()
			timing.confirmationBaseline = nonNegativeDuration(e.now().Sub(baselineStartedAt))
			if err == nil {
				baselineReady = true
			} else {
				timing.confirmationOutcome = "baseline_unavailable"
				err = nil // The prompt is still submitted once; failure is classified afterward.
			}
		}
		startedAt := e.now()
		err = e.driver.SendLiteral(ctx, binding.TmuxTarget, request.OperationID, text)
		timing.tmuxSend = nonNegativeDuration(e.now().Sub(startedAt))
		if err != nil {
			timing.failureStage = "tmux_send"
			break
		}
		if confirmer != nil && baselineReady {
			confirmationCtx, cancel := context.WithTimeout(ctx, inputConfirmationTimeout)
			confirmationStartedAt := e.now()
			confirmationErr := confirmer.ConfirmInput(
				confirmationCtx, binding, baseline, promptidentity.Digest(strings.TrimSpace(text)),
			)
			cancel()
			timing.confirmation = nonNegativeDuration(e.now().Sub(confirmationStartedAt))
			switch {
			case confirmationErr == nil:
				timing.confirmationOutcome = "confirmed"
				if baseline.LegacyGeneration {
					timing.confirmationOutcome = "confirmed_legacy"
				}
				accepted := true
				result.ProviderAccepted = &accepted
			case errors.Is(confirmationErr, ErrStaleRuntime):
				timing.confirmationOutcome = "stale_generation"
			default:
				timing.confirmationOutcome = "unconfirmed"
			}
		}
		if confirmer != nil && timing.confirmationOutcome != "confirmed" &&
			timing.confirmationOutcome != "confirmed_legacy" {
			accepted := false
			result.ProviderAccepted = &accepted
			timing.failureStage = "confirmation"
			err = ErrInputUnconfirmed
		}
	case ActionStop:
		err = e.driver.SendKey(ctx, binding.TmuxTarget, "Escape")
	case ActionClear:
		err = e.clear(ctx, request.OperationID, binding)
		if err == nil {
			result.ResetNaming = true
			result.ResetProviderBinding = true
		}
	case ActionClose:
		e.mu.RLock()
		archiver := e.archiver
		e.mu.RUnlock()
		if archiver == nil {
			err = ErrRuntimeUnavailable
			break
		}
		err = archiver.Commit(ctx, request)
		if err == nil {
			result.ArchiveCommitted = true
			err = e.driver.Close(ctx, binding.TmuxTarget)
		}
		if err == nil {
			err = archiver.Finalize(ctx, request)
		}
	case ActionDiscard:
		err = e.driver.Close(ctx, binding.TmuxTarget)
	case ActionOpenTerminal:
		err = e.driver.OpenTerminal(ctx, binding.TmuxTarget)
	case ActionCapture:
		result.Pane, err = e.driver.CapturePane(ctx, binding.TmuxTarget)
	case ActionSendKey:
		result.Pane, err = e.sendInteractiveKey(ctx, request, binding)
	case ActionGenerateName:
		e.mu.RLock()
		namer := e.namer
		e.mu.RUnlock()
		if namer == nil {
			err = ErrUnsupportedBackendAction
			break
		}
		result.GeneratedName, err = namer.Generate(ctx, binding.Backend, request.Text)
	}
	if err != nil {
		if errors.Is(err, ErrInputUnconfirmed) {
			result.Detail = "provider did not confirm submitted input"
		} else {
			result.Detail = "runtime operation failed"
		}
		return result, timing, err
	}
	result.Delivered = true
	result.Detail = "runtime operation delivered"
	if request.Action == ActionClose || request.Action == ActionDiscard {
		e.retireClosedRuntime(binding)
	}
	return result, timing, nil
}

func (e *LocalExecutor) sendInteractiveKey(
	ctx context.Context,
	request Request,
	binding RuntimeBinding,
) ([]byte, error) {
	pane, err := e.driver.CapturePane(ctx, binding.TmuxTarget)
	if err != nil {
		return nil, err
	}
	prompt, ok := interactive.Detect(pane)
	if !ok || prompt.Hash != request.ExpectedPromptHash {
		return nil, ErrStaleRuntime
	}
	key, ok := tmuxInteractiveKey(request.Key)
	if !ok {
		return nil, ErrUnsupportedBackendAction
	}
	if err := e.driver.SendKey(ctx, binding.TmuxTarget, key); err != nil {
		return nil, err
	}
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return e.driver.CapturePane(ctx, binding.TmuxTarget)
}

func tmuxInteractiveKey(key InteractiveKey) (string, bool) {
	switch key {
	case KeyUp:
		return "Up", true
	case KeyDown:
		return "Down", true
	case KeyLeft:
		return "Left", true
	case KeyRight:
		return "Right", true
	case KeyEnter:
		return "Enter", true
	case KeyEscape:
		return "Escape", true
	case KeySpace:
		return "Space", true
	case KeyTab:
		return "Tab", true
	case KeyCtrlC:
		return "C-c", true
	default:
		return "", false
	}
}

func (e *LocalExecutor) clear(
	ctx context.Context,
	operationID string,
	binding RuntimeBinding,
) error {
	command, err := backendClearCommand(binding.Backend)
	if err != nil {
		return err
	}
	if err := e.driver.SendKey(ctx, binding.TmuxTarget, "Escape"); err != nil {
		return err
	}
	return e.driver.SendLiteral(ctx, binding.TmuxTarget, operationID+"-clear", command)
}

func backendClearCommand(backend string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "claude":
		return "/clear", nil
	case "codex":
		return "/new", nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedBackendAction, backend)
	}
}
