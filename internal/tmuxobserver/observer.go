// Package tmuxobserver provides read-only terminal change notifications from
// a tmux control-mode client. It never exposes terminal payloads.
package tmuxobserver

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxControlLineBytes = 64 << 10
	attachTimeout       = 5 * time.Second
	heartbeatInterval   = 75 * time.Millisecond
	heartbeatMisses     = 2
)

var (
	errInvalidSpec        = errors.New("invalid tmux observer specification")
	errAttachRejected     = errors.New("tmux observer attach rejected")
	errAttachDisconnected = errors.New("tmux observer disconnected before attach")
	errAttachTimeout      = errors.New("tmux observer attach timed out")
	errDisconnected       = errors.New("tmux observer disconnected")
	errProtocolRead       = errors.New("tmux observer protocol read failed")
)

type Spec struct {
	Executable  string
	Socket      string
	Target      string
	Environment []string
}

type Observer struct {
	updates chan struct{}
	done    chan error
	attach  chan error
	reaped  chan struct{}

	stdin   io.WriteCloser
	cancel  context.CancelFunc
	command *exec.Cmd
	target  string

	closing   atomic.Bool
	watchdog  atomic.Bool
	heartbeat atomic.Uint64
	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

func Start(ctx context.Context, specification Spec) (*Observer, error) {
	if ctx == nil || specification.Executable == "" || specification.Socket == "" || !validTarget(specification.Target) {
		return nil, errInvalidSpec
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	processContext, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(processContext, specification.Executable,
		"-u", "-N", "-C", "-S", specification.Socket,
		"attach-session", "-r", "-t", specification.Target,
	)
	if specification.Environment != nil {
		command.Env = append([]string{}, specification.Environment...)
	}
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		cancel()
		return nil, err
	}
	observer := &Observer{
		updates: make(chan struct{}, 1), done: make(chan error, 1),
		attach: make(chan error, 1), reaped: make(chan struct{}),
		stdin: stdin, cancel: cancel, command: command, target: specification.Target,
	}
	go observer.run(ctx, command, stdout)
	timer := time.NewTimer(attachTimeout)
	defer timer.Stop()
	select {
	case err := <-observer.attach:
		if err != nil {
			observer.closing.Store(true)
			observer.stop()
			<-observer.reaped
			return nil, err
		}
		return observer, nil
	case <-ctx.Done():
		observer.closing.Store(true)
		observer.stop()
		<-observer.reaped
		return nil, ctx.Err()
	case <-timer.C:
		observer.closing.Store(true)
		observer.stop()
		<-observer.reaped
		return nil, errAttachTimeout
	}
}

func (observer *Observer) Updates() <-chan struct{} { return observer.updates }

func (observer *Observer) Done() <-chan error { return observer.done }

func (observer *Observer) Close() error {
	if observer == nil {
		return nil
	}
	observer.closeOnce.Do(func() {
		observer.closing.Store(true)
		observer.stop()
		<-observer.reaped
	})
	return observer.closeErr
}

func (observer *Observer) run(parent context.Context, command *exec.Cmd, output io.ReadCloser) {
	attached := false
	terminal := false
	attachStarted := false
	var attachIdentity [3]string
	heartbeatStarted := false
	var heartbeatIdentity [3]string
	var stopHeartbeat chan struct{}
	var heartbeatDone chan struct{}
	var result error
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), maxControlLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !attached {
			if identity, ok := commandIdentity(line, "%begin"); ok && !attachStarted {
				attachStarted = true
				attachIdentity = identity
			} else if identity, ok := commandIdentity(line, "%end"); ok && attachStarted && identity == attachIdentity {
				attached = true
				observer.hint()
				observer.attach <- nil
				stopHeartbeat = make(chan struct{})
				heartbeatDone = make(chan struct{})
				go observer.runHeartbeat(stopHeartbeat, heartbeatDone)
			} else if identity, ok := commandIdentity(line, "%error"); ok && attachStarted && identity == attachIdentity {
				result = errAttachRejected
			}
		} else {
			switch {
			case bytes.HasPrefix(line, []byte("%output ")):
				observer.hint()
			case !heartbeatStarted:
				if identity, ok := commandIdentity(line, "%begin"); ok {
					heartbeatStarted = true
					heartbeatIdentity = identity
				}
			case bytes.Equal(line, []byte("1")):
				terminal = true
			default:
				if identity, ok := commandIdentity(line, "%end"); ok && identity == heartbeatIdentity {
					heartbeatStarted = false
					observer.heartbeat.Add(1)
				} else if identity, ok := commandIdentity(line, "%error"); ok && identity == heartbeatIdentity {
					result = errDisconnected
				}
			}
		}
		if controlLine(line, "%exit") || bytes.HasPrefix(line, []byte("%pane-exited ")) || bytes.HasPrefix(line, []byte("%window-close ")) {
			terminal = true
		}
		if result != nil || terminal {
			break
		}
	}
	if observer.watchdog.Load() {
		result = errDisconnected
	} else if result == nil && scanner.Err() != nil {
		result = errProtocolRead
	}
	if result == nil && terminal && !attached {
		result = errAttachDisconnected
	}
	if result == nil && !terminal {
		switch {
		case parent.Err() != nil:
			result = parent.Err()
		case observer.closing.Load():
			result = nil
		case !attached:
			result = errAttachDisconnected
		default:
			result = errDisconnected
		}
	}
	if !attached {
		observer.attach <- result
	}
	if stopHeartbeat != nil {
		close(stopHeartbeat)
		<-heartbeatDone
	}
	observer.stop()
	waitErr := command.Wait()
	if result == nil && !terminal && !observer.closing.Load() && waitErr != nil {
		result = errDisconnected
	}
	observer.done <- result
	close(observer.done)
	close(observer.updates)
	close(observer.reaped)
}

func (observer *Observer) runHeartbeat(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	lastAck := observer.heartbeat.Load()
	misses := 0
	send := func() bool {
		_, err := io.WriteString(observer.stdin, "display-message -p -t "+observer.target+" \"#{pane_dead}\"\n")
		if err == nil {
			return true
		}
		if !observer.closing.Load() {
			observer.watchdog.Store(true)
			observer.cancel()
		}
		return false
	}
	if !send() {
		return
	}
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ack := observer.heartbeat.Load()
			if ack == lastAck {
				misses++
			} else {
				lastAck = ack
				misses = 0
			}
			if misses >= heartbeatMisses {
				if !observer.closing.Load() {
					observer.watchdog.Store(true)
					observer.cancel()
				}
				return
			}
			if !send() {
				return
			}
		}
	}
}

func (observer *Observer) hint() {
	select {
	case observer.updates <- struct{}{}:
	default:
	}
}

func (observer *Observer) stop() {
	observer.stopOnce.Do(func() {
		_ = observer.stdin.Close()
		observer.cancel()
	})
}

func controlLine(line []byte, name string) bool {
	prefix := []byte(name)
	return bytes.Equal(line, prefix) || len(line) > len(prefix) && bytes.Equal(line[:len(prefix)], prefix) && line[len(prefix)] == ' '
}

func commandIdentity(line []byte, name string) ([3]string, bool) {
	prefix := []byte(name + " ")
	if !bytes.HasPrefix(line, prefix) {
		return [3]string{}, false
	}
	fields := bytes.Fields(line)
	if len(fields) != 4 || !bytes.Equal(fields[0], []byte(name)) {
		return [3]string{}, false
	}
	return [3]string{string(fields[1]), string(fields[2]), string(fields[3])}, true
}

func validTarget(target string) bool {
	return target != "" && strings.IndexFunc(target, func(character rune) bool {
		return character > 127 || !(character >= 'a' && character <= 'z') &&
			!(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') &&
			character != ':' && character != '.' && character != '_' && character != '-'
	}) == -1
}
