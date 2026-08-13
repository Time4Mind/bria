//go:build !windows

package providerauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"

	"github.com/creack/pty"
)

var claudeURLPattern = regexp.MustCompile(`https://claude\.com/cai/oauth/authorize\?[^\s\x1b\]]+`)

type claudeProcess struct {
	command *exec.Cmd
	pty     *os.File
	url     string

	chunks chan []byte
	exit   chan error
	once   sync.Once
}

func launchClaude(ctx context.Context, executable string) (Process, error) {
	command := exec.Command(executable, "auth", "login")
	command.Env = authenticationEnvironment()
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 50, Cols: 400})
	if err != nil {
		return nil, err
	}
	process := &claudeProcess{
		command: command, pty: terminal, chunks: make(chan []byte, 16), exit: make(chan error, 1),
	}
	go process.read()
	go func() { process.exit <- command.Wait(); close(process.exit) }()
	var output bytes.Buffer
	for {
		select {
		case <-ctx.Done():
			_ = process.Cancel()
			return nil, errors.New("Claude did not provide an authorization URL")
		case chunk, ok := <-process.chunks:
			if !ok {
				_ = process.Cancel()
				return nil, errors.New("Claude exited before providing an authorization URL")
			}
			_, _ = output.Write(chunk)
			if output.Len() > 64<<10 {
				_ = process.Cancel()
				return nil, errors.New("Claude authorization output exceeded its limit")
			}
			if match := claudeURLPattern.Find(output.Bytes()); len(match) != 0 {
				process.url = string(match)
				return process, nil
			}
		case <-process.exit:
			_ = process.Cancel()
			return nil, errors.New("Claude exited before providing an authorization URL")
		}
	}
}

func (p *claudeProcess) Challenge() (string, string, bool) { return p.url, "", true }

func (p *claudeProcess) Submit(_ context.Context, code string) error {
	_, err := io.WriteString(p.pty, code+"\r")
	return err
}

func (p *claudeProcess) Wait() (bool, string) {
	var tail bytes.Buffer
	chunks := p.chunks
	for {
		select {
		case chunk, ok := <-chunks:
			if ok {
				_, _ = tail.Write(chunk)
				if tail.Len() > 4096 {
					data := append([]byte(nil), tail.Bytes()[tail.Len()-2048:]...)
					tail.Reset()
					_, _ = tail.Write(data)
				}
			} else {
				chunks = nil
			}
		case err, ok := <-p.exit:
			if !ok || err == nil {
				_ = p.Cancel()
				return true, ""
			}
			_ = p.Cancel()
			return false, string(tail.Bytes())
		}
	}
}

func (p *claudeProcess) Cancel() error {
	var result error
	p.once.Do(func() {
		if p.command.Process != nil {
			result = p.command.Process.Kill()
		}
		if err := p.pty.Close(); result == nil && err != nil {
			result = err
		}
	})
	return result
}

func (p *claudeProcess) read() {
	defer close(p.chunks)
	buffer := make([]byte, 4096)
	for {
		count, err := p.pty.Read(buffer)
		if count > 0 {
			p.chunks <- append([]byte(nil), buffer[:count]...)
		}
		if err != nil {
			return
		}
	}
}
