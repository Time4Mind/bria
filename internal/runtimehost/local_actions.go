package runtimehost

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/interactive"
)

func (e *LocalExecutor) executeOnce(
	ctx context.Context,
	request Request,
	binding RuntimeBinding,
) (Result, error) {
	if current, err := e.resolveSession(request); err != nil || current.snapshot() != binding {
		return Result{Accepted: true, Detail: "runtime target became stale"}, ErrStaleRuntime
	}
	result := Result{Accepted: true, Detail: "runtime operation accepted"}
	var err error
	switch request.Action {
	case ActionSendInput:
		text := request.Text
		if request.Input != nil {
			e.mu.RLock()
			resolver := e.inputs
			e.mu.RUnlock()
			if resolver == nil {
				err = ErrUnsupportedBackendAction
				break
			}
			text, err = resolver.ResolveInput(ctx, binding.Workdir, *request.Input)
			if err != nil {
				break
			}
			result.ResolvedText = text
		}
		err = e.driver.SendLiteral(ctx, binding.TmuxTarget, request.OperationID, text)
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
		result.Detail = "runtime operation failed"
		return result, err
	}
	result.Delivered = true
	result.Detail = "runtime operation delivered"
	if request.Action == ActionClose {
		e.retireClosedRuntime(binding)
	}
	return result, nil
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
