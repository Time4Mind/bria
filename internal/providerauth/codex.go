package providerauth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

const maxCodexMessageBytes = 1 << 20

type codexProcess struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	messages chan map[string]any
	exit     chan error
	done     chan struct{}
	loginID  string
	url      string
	userCode string
	writeMu  sync.Mutex
	once     sync.Once
}

func launchCodex(ctx context.Context, executable string) (Process, error) {
	command := exec.Command(executable, "app-server")
	command.Env = authenticationEnvironment()
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	process := &codexProcess{
		command: command, stdin: stdin, messages: make(chan map[string]any, 16),
		exit: make(chan error, 1), done: make(chan struct{}),
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	go process.scan(stdout)
	go func() { process.exit <- command.Wait(); close(process.exit) }()
	if err := process.send(map[string]any{
		"method": "initialize", "id": 0,
		"params": map[string]any{"clientInfo": map[string]string{
			"name": "bria", "title": "Bria", "version": "1",
		}},
	}); err != nil {
		_ = process.Cancel()
		return nil, err
	}
	response, err := process.response(ctx, 0)
	if err != nil || response["error"] != nil {
		_ = process.Cancel()
		return nil, errors.New("Codex app-server initialization failed")
	}
	if err := process.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		_ = process.Cancel()
		return nil, err
	}
	if err := process.send(map[string]any{
		"method": "account/login/start", "id": 1,
		"params": map[string]string{"type": "chatgptDeviceCode"},
	}); err != nil {
		_ = process.Cancel()
		return nil, err
	}
	response, err = process.response(ctx, 1)
	result, _ := response["result"].(map[string]any)
	if err != nil || response["error"] != nil || result == nil {
		_ = process.Cancel()
		return nil, errors.New("Codex did not start device authentication")
	}
	process.loginID, _ = result["loginId"].(string)
	process.url, _ = result["verificationUrl"].(string)
	process.userCode, _ = result["userCode"].(string)
	if process.loginID == "" || process.url == "" || process.userCode == "" {
		_ = process.Cancel()
		return nil, errors.New("Codex returned an incomplete device challenge")
	}
	return process, nil
}

func (p *codexProcess) Challenge() (string, string, bool) {
	return p.url, p.userCode, false
}

func (p *codexProcess) Submit(context.Context, string) error { return ErrFlowNotWaiting }

func (p *codexProcess) Wait() (bool, string) {
	for {
		select {
		case message, ok := <-p.messages:
			if !ok || message["method"] != "account/login/completed" {
				if !ok {
					return false, "Codex app-server closed"
				}
				continue
			}
			params, _ := message["params"].(map[string]any)
			if params == nil || params["loginId"] != p.loginID {
				continue
			}
			_ = p.Cancel()
			if success, _ := params["success"].(bool); success {
				return true, ""
			}
			return false, fmt.Sprint(params["error"])
		case err, ok := <-p.exit:
			if !ok || err == nil {
				return false, "Codex app-server exited before authentication completed"
			}
			return false, err.Error()
		}
	}
}

func (p *codexProcess) Cancel() error {
	var result error
	p.once.Do(func() {
		close(p.done)
		_ = p.stdin.Close()
		if p.command.Process != nil {
			result = p.command.Process.Kill()
		}
	})
	return result
}

func (p *codexProcess) send(message any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return json.NewEncoder(p.stdin).Encode(message)
}

func (p *codexProcess) response(ctx context.Context, id float64) (map[string]any, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case message, ok := <-p.messages:
			if !ok {
				return nil, errors.New("Codex app-server closed")
			}
			if message["id"] == id {
				return message, nil
			}
		case err, ok := <-p.exit:
			if !ok || err == nil {
				return nil, errors.New("Codex app-server exited")
			}
			return nil, err
		}
	}
}

func (p *codexProcess) scan(reader io.Reader) {
	defer close(p.messages)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxCodexMessageBytes)
	for scanner.Scan() {
		var message map[string]any
		if json.Unmarshal(scanner.Bytes(), &message) == nil {
			select {
			case p.messages <- message:
			case <-p.done:
				return
			}
		}
	}
}
