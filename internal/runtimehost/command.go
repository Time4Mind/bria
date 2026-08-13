// Package runtimehost detects and drives software installed on one host.
package runtimehost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type CommandRunner interface {
	LookPath(string) (string, error)
	Run(context.Context, string, ...string) (CommandResult, error)
}

// InputCommandRunner runs a command with an exact byte stream on stdin. It is
// kept separate from CommandRunner so probes and other read-only callers do
// not need to manufacture an input stream.
type InputCommandRunner interface {
	CommandRunner
	RunInput(context.Context, []byte, string, ...string) (CommandResult, error)
}

// JSONRPCCommandRunner drives line-delimited JSON-RPC servers that require
// initialize to complete before accepting later requests.
type JSONRPCCommandRunner interface {
	InputCommandRunner
	RunJSONRPC(
		context.Context, []byte, []byte, int, string, ...string,
	) (CommandResult, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	return runCommand(ctx, nil, name, args...)
}

func (ExecCommandRunner) RunInput(
	ctx context.Context,
	input []byte,
	name string,
	args ...string,
) (CommandResult, error) {
	return runCommand(ctx, bytes.NewReader(input), name, args...)
}

func (ExecCommandRunner) RunJSONRPC(
	ctx context.Context,
	initialize []byte,
	requests []byte,
	expectedID int,
	name string,
	args ...string,
) (CommandResult, error) {
	command := exec.CommandContext(ctx, name, args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return CommandResult{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return CommandResult{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return CommandResult{}, err
	}
	if err := command.Start(); err != nil {
		return CommandResult{}, err
	}
	var stderrBuffer bytes.Buffer
	var stderrWait sync.WaitGroup
	stderrWait.Add(1)
	go func() {
		defer stderrWait.Done()
		_, _ = io.Copy(&stderrBuffer, stderr)
	}()
	lines := make(chan []byte, 8)
	scanErrors := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			lines <- append([]byte(nil), scanner.Bytes()...)
		}
		scanErrors <- scanner.Err()
		close(lines)
	}()
	write := func(data []byte) error {
		if _, writeErr := stdin.Write(data); writeErr != nil {
			return writeErr
		}
		return nil
	}
	if err := write(initialize); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		stderrWait.Wait()
		return CommandResult{Stderr: stderrBuffer.Bytes()}, err
	}
	var output bytes.Buffer
	requestsSent := false
	found := false
	for !found {
		select {
		case <-ctx.Done():
			_ = command.Process.Kill()
			_ = command.Wait()
			stderrWait.Wait()
			return CommandResult{Stdout: output.Bytes(), Stderr: stderrBuffer.Bytes()}, ctx.Err()
		case line, ok := <-lines:
			if !ok {
				scanErr := <-scanErrors
				_ = command.Wait()
				stderrWait.Wait()
				if scanErr != nil {
					return CommandResult{Stdout: output.Bytes(), Stderr: stderrBuffer.Bytes()}, scanErr
				}
				return CommandResult{Stdout: output.Bytes(), Stderr: stderrBuffer.Bytes()}, nil
			}
			output.Write(line)
			output.WriteByte('\n')
			var response struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(line, &response) != nil {
				continue
			}
			if response.ID == 0 && !requestsSent {
				if err := write(requests); err != nil {
					_ = command.Process.Kill()
					_ = command.Wait()
					stderrWait.Wait()
					return CommandResult{Stdout: output.Bytes(), Stderr: stderrBuffer.Bytes()}, err
				}
				requestsSent = true
			}
			found = response.ID == expectedID
		}
	}
	_ = stdin.Close()
	drained := make(chan error, 1)
	go func() {
		for range lines {
		}
		drained <- <-scanErrors
	}()
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-time.After(2 * time.Second):
		_ = command.Process.Kill()
		waitErr = <-waited
	}
	scanErr := <-drained
	stderrWait.Wait()
	result := CommandResult{Stdout: output.Bytes(), Stderr: stderrBuffer.Bytes()}
	if scanErr != nil {
		return result, scanErr
	}
	var exitError *exec.ExitError
	if waitErr != nil && errors.As(waitErr, &exitError) {
		result.ExitCode = exitError.ExitCode()
		// The expected response is complete; terminating a server that elects
		// to stay resident is not a failed JSON-RPC exchange.
		if result.ExitCode == -1 {
			result.ExitCode = 0
		}
		return result, nil
	}
	return result, waitErr
}

func runCommand(
	ctx context.Context,
	stdin *bytes.Reader,
	name string,
	args ...string,
) (CommandResult, error) {
	command := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		command.Stdin = stdin
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}
