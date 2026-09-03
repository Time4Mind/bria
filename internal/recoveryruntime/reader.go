// Package recoveryruntime runs bounded provider adapters as read-only,
// untracked accepted-turn history readers.
package recoveryruntime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/processgroup"
	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
)

var ErrUnavailable = errors.New("accepted-turn reconciliation is unavailable")

const (
	defaultHandshakeTimeout = 5 * time.Second
	defaultCloseTimeout     = 2 * time.Second
	defaultTerminateTimeout = 2 * time.Second
	defaultMaxLineBytes     = 64 * 1024
	defaultMaxTextBytes     = 32 * 1024
	defaultMaxTurns         = 10_000
)

type Options struct {
	HandshakeTimeout time.Duration
	CloseTimeout     time.Duration
	TerminateTimeout time.Duration
	MaxLineBytes     int
	MaxTextBytes     int
	MaxTurns         int
}

type Reader struct {
	command          sessionruntime.CommandSpec
	identity         os.FileInfo
	handshakeTimeout time.Duration
	closeTimeout     time.Duration
	terminateTimeout time.Duration
	maxLineBytes     int
	maxTextBytes     int
	maxTurns         int
	requestSequence  atomic.Uint64
}

func New(command sessionruntime.CommandSpec, options Options) (*Reader, error) {
	validated, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: command}, sessionruntime.Options{
		HandshakeTimeout: options.HandshakeTimeout, GracefulCloseTimeout: options.CloseTimeout,
		GracefulTerminateTimeout: options.TerminateTimeout, MaxLineBytes: options.MaxLineBytes,
		MaxTextBytes: options.MaxTextBytes, MaxReconciledTurns: options.MaxTurns,
	})
	if err != nil || validated == nil {
		return nil, ErrUnavailable
	}
	resolved, err := filepath.EvalSymlinks(command.Path)
	if err != nil {
		return nil, ErrUnavailable
	}
	identity, err := os.Stat(resolved)
	if err != nil {
		return nil, ErrUnavailable
	}
	command.Path = resolved
	command.Args = append([]string(nil), command.Args...)
	command.Env = append([]string(nil), command.Env...)
	reader := &Reader{command: command, identity: identity}
	reader.handshakeTimeout = valueOr(options.HandshakeTimeout, defaultHandshakeTimeout)
	reader.closeTimeout = valueOr(options.CloseTimeout, defaultCloseTimeout)
	reader.terminateTimeout = valueOr(options.TerminateTimeout, defaultTerminateTimeout)
	reader.maxLineBytes = intOr(options.MaxLineBytes, defaultMaxLineBytes)
	reader.maxTextBytes = intOr(options.MaxTextBytes, defaultMaxTextBytes)
	reader.maxTurns = intOr(options.MaxTurns, defaultMaxTurns)
	return reader, nil
}

// ReadAcceptedTurns starts the configured provider adapter as an untracked
// one-shot reader for an exact persisted binding. The adapter owns
// provider-specific history access; sessionruntime only validates the bounded
// provider-neutral receipt stream.
func (reader *Reader) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	binding := request.Binding
	if reader == nil || ctx == nil || strings.TrimSpace(string(request.SessionID)) == "" || request.Provider != domain.ProviderCodex ||
		binding.Provider != request.Provider || binding.SessionID == "" || binding.Generation == 0 ||
		!utf8.ValidString(binding.SessionID) || strings.ContainsRune(binding.SessionID, '\x00') ||
		!filepath.IsAbs(request.Workdir) || !utf8.ValidString(request.Workdir) || strings.ContainsRune(request.Workdir, '\x00') {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	current, err := os.Stat(reader.command.Path)
	if err != nil || !os.SameFile(current, reader.identity) {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	command := exec.Command(reader.command.Path, reader.command.Args...)
	command.Dir = request.Workdir
	command.Env = append([]string(nil), reader.command.Env...)
	command.Env = append(command.Env,
		"BRIA_SESSION_ID="+string(request.SessionID),
		"BRIA_PROVIDER="+string(binding.Provider),
		sessionruntime.EnvironmentStartMode+"=resume",
		sessionruntime.EnvironmentGeneration+"="+strconv.FormatUint(binding.Generation, 10),
		sessionruntime.EnvironmentProviderSession+"="+binding.SessionID,
	)
	if reader.command.ProviderCredentialFile != "" {
		command.Env = append(command.Env, sessionruntime.EnvironmentProviderCredentialFile+"="+reader.command.ProviderCredentialFile)
	}
	if err := processgroup.Configure(command); err != nil {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	if err := command.Start(); err != nil {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	output := make(chan wireResult, 32)
	outputEOF := make(chan struct{})
	stopReader := make(chan struct{})
	go readOutput(stdout, reader.maxLineBytes, output, outputEOF, stopReader)
	cleaned := false
	cleanup := func(graceful bool) {
		if cleaned {
			return
		}
		cleaned = true
		if graceful {
			line, encodeErr := runtimeprotocol.EncodeParentLine(runtimeprotocol.ParentMessage{
				Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeClose,
			}, runtimeprotocol.Limits{MaxLineBytes: reader.maxLineBytes, MaxTextBytes: reader.maxTextBytes})
			if encodeErr == nil {
				_, _ = stdin.Write(line)
			}
		}
		_ = stdin.Close()
		timer := time.NewTimer(reader.closeTimeout)
		select {
		case <-outputEOF:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = processgroup.TerminateTree(command)
			terminateTimer := time.NewTimer(reader.terminateTimeout)
			select {
			case <-outputEOF:
				if !terminateTimer.Stop() {
					<-terminateTimer.C
				}
			case <-terminateTimer.C:
				close(stopReader)
				_ = processgroup.KillTree(command)
				<-outputEOF
			}
		}
		_ = processgroup.KillTree(command)
		_ = command.Wait()
	}
	defer cleanup(false)

	readyTimer := time.NewTimer(reader.handshakeTimeout)
	defer readyTimer.Stop()
	select {
	case frame, open := <-output:
		if !open || frame.err != nil || frame.message.Type != runtimeprotocol.TypeReady || frame.message.ProviderSessionID != binding.SessionID {
			return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
		}
	case <-readyTimer.C:
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	case <-ctx.Done():
		return sessionruntime.AcceptedTurnReconciliation{}, ctx.Err()
	}

	requestID := fmt.Sprintf("reconcile-%d", reader.requestSequence.Add(1))
	line, err := runtimeprotocol.EncodeParentLine(runtimeprotocol.ParentMessage{
		Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeReconcileAcceptedTurns, RequestID: requestID,
	}, runtimeprotocol.Limits{MaxLineBytes: reader.maxLineBytes, MaxTextBytes: reader.maxTextBytes})
	if err != nil || writeOneShot(ctx, stdin, line) != nil {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	receipt := sessionruntime.AcceptedTurnReconciliation{Turns: make([]sessionruntime.ReconciledAcceptedTurn, 0)}
	seen := make(map[string]struct{})
	for {
		select {
		case frame, open := <-output:
			if !open || frame.err != nil || frame.message.RequestID != requestID {
				return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
			}
			switch frame.message.Type {
			case runtimeprotocol.TypeAcceptedTurn:
				if len(receipt.Turns) >= reader.maxTurns {
					return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
				}
				if _, duplicate := seen[frame.message.MessageID]; duplicate {
					return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
				}
				seen[frame.message.MessageID] = struct{}{}
				receipt.Turns = append(receipt.Turns, sessionruntime.ReconciledAcceptedTurn{
					MessageID: frame.message.MessageID, Outcome: sessionruntime.AcceptedTurnOutcome(frame.message.Status),
				})
			case runtimeprotocol.TypeReconciliationCompleted:
				cleanup(true)
				return receipt, nil
			default:
				return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
			}
		case <-ctx.Done():
			return sessionruntime.AcceptedTurnReconciliation{}, ctx.Err()
		}
	}
}

func writeOneShot(ctx context.Context, output io.Writer, line []byte) error {
	done := make(chan error, 1)
	go func() {
		written, err := output.Write(line)
		if err == nil && written != len(line) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type wireResult struct {
	message runtimeprotocol.AdapterMessage
	err     error
}

func readOutput(stdout io.ReadCloser, maxLineBytes int, output chan<- wireResult, done chan<- struct{}, stop <-chan struct{}) {
	defer close(output)
	defer close(done)
	defer stdout.Close()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, min(maxLineBytes, 4096)), maxLineBytes)
	for scanner.Scan() {
		message, err := runtimeprotocol.DecodeAdapterLine(scanner.Bytes(), runtimeprotocol.Limits{MaxLineBytes: maxLineBytes})
		select {
		case output <- wireResult{message: message, err: err}:
		case <-stop:
			return
		}
		if err != nil {
			return
		}
	}
}

func valueOr(value, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return value
}

func intOr(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

var _ sessionruntime.AcceptedTurnReader = (*Reader)(nil)
