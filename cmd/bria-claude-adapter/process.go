package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"bria/internal/processgroup"
	"bria/internal/provider/claude"
)

type osProcessFactory struct {
	mu                      sync.Mutex
	needsCurrentTreeCleanup bool
	credentialPath          string
}

func (factory *osProcessFactory) Start(ctx context.Context, spec claude.CommandSpec) (claude.ChildProcess, error) {
	if ctx == nil || ctx.Err() != nil || !spec.ExecutableMatches() {
		return nil, claude.ErrChildStart
	}
	command := exec.Command(spec.Path, spec.Args...)
	command.Dir = spec.Workdir
	command.Stderr = io.Discard
	environment, err := spec.EnvironmentWithStoredAPIKey(os.Environ(), factory.credentialPath)
	if err != nil {
		return nil, claude.ErrChildStart
	}
	command.Env = environment
	inherited, err := processgroup.ConfigureDescendant(command)
	if err != nil {
		return nil, claude.ErrChildStart
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, claude.ErrChildStart
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, claude.ErrChildStart
	}
	if !spec.ExecutableMatches() {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, claude.ErrChildStart
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, claude.ErrChildStart
	}
	factory.mu.Lock()
	factory.needsCurrentTreeCleanup = inherited
	factory.mu.Unlock()
	child := &osChild{command: command, stdin: stdin, done: make(chan error, 1), inherited: inherited}
	child.stdout = &childOutput{source: stdout, onEOF: child.reapNatural}
	go func() {
		select {
		case <-ctx.Done():
			_ = child.Kill()
		case <-child.done:
		}
	}()
	return child, nil
}

type osChild struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	stdout    *childOutput
	done      chan error
	inherited bool

	lifecycleMu sync.Mutex
	reaping     bool
}

func (child *osChild) Stdin() io.WriteCloser { return child.stdin }
func (child *osChild) Stdout() io.ReadCloser { return child.stdout }
func (child *osChild) Done() <-chan error    { return child.done }
func (child *osChild) Kill() error {
	child.lifecycleMu.Lock()
	if child.reaping {
		child.lifecycleMu.Unlock()
		<-child.done
		return nil
	}
	child.reaping = true
	signalErr := child.signalBeforeWait()
	waitErr := child.command.Wait()
	reaped := child.command.ProcessState != nil
	permissionRaceConfirmedGone := false
	if reaped && !child.inherited && errors.Is(signalErr, os.ErrPermission) {
		// Darwin can report EPERM when a dedicated group disappears between the
		// pre-Wait identity probe and SIGKILL. Only a non-signalling post-reap
		// ESRCH confirmation makes that race benign. Any live, reused, or
		// uninspectable group remains a fail-closed stop error.
		permissionRaceConfirmedGone = processgroup.ConfirmTreeGone(child.command) == nil
	}
	child.publishDone(waitErr)
	child.lifecycleMu.Unlock()
	if !reaped {
		return claude.ErrChildStop
	}
	if signalErr != nil && !permissionRaceConfirmedGone {
		return fmt.Errorf("%w: %v", claude.ErrChildStop, signalErr)
	}
	return nil
}

func (child *osChild) reapNatural() {
	child.lifecycleMu.Lock()
	if child.reaping {
		child.lifecycleMu.Unlock()
		return
	}
	child.reaping = true
	_ = child.signalBeforeWait()
	waitErr := child.command.Wait()
	child.publishDone(waitErr)
	child.lifecycleMu.Unlock()
}

func (child *osChild) signalBeforeWait() error {
	if child.command.Process == nil {
		return nil
	}
	if child.inherited {
		err := child.command.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	return processgroup.KillTree(child.command)
}

func (child *osChild) publishDone(err error) {
	child.done <- err
	close(child.done)
}

type childOutput struct {
	source io.ReadCloser
	onEOF  func()

	mu            sync.Mutex
	locallyClosed bool
	eofOnce       sync.Once
}

func (output *childOutput) Read(buffer []byte) (int, error) {
	count, err := output.source.Read(buffer)
	if errors.Is(err, io.EOF) {
		output.mu.Lock()
		locallyClosed := output.locallyClosed
		output.mu.Unlock()
		if !locallyClosed {
			output.eofOnce.Do(func() { go output.onEOF() })
		}
	}
	return count, err
}

func (output *childOutput) Close() error {
	output.mu.Lock()
	output.locallyClosed = true
	output.mu.Unlock()
	return output.source.Close()
}

func (factory *osProcessFactory) cleanupCurrentTree() error {
	factory.mu.Lock()
	required := factory.needsCurrentTreeCleanup
	factory.mu.Unlock()
	if !required {
		return nil
	}
	return processgroup.KillCurrentTree()
}
