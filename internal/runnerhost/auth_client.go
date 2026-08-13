package runnerhost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/providerauth"
)

type AuthLauncher struct {
	client   *Client
	commands map[string]providerauth.Command
}

func NewAuthLauncher(
	client *Client,
	commands map[string]providerauth.Command,
) (*AuthLauncher, error) {
	if client == nil || len(commands) == 0 {
		return nil, errors.New("runner authentication dependencies are required")
	}
	copyCommands := make(map[string]providerauth.Command, len(commands))
	for backend, command := range commands {
		if strings.TrimSpace(command.Executable) == "" {
			return nil, errors.New("runner authentication executable is required")
		}
		copyCommands[backend] = command
	}
	return &AuthLauncher{client: client, commands: copyCommands}, nil
}

func (l *AuthLauncher) Launch(
	ctx context.Context,
	backend string,
	_ string,
) (providerauth.Process, error) {
	command, ok := l.commands[backend]
	if !ok {
		return nil, providerauth.ErrUnsupportedBackend
	}
	var response authStartResponse
	if err := l.client.call(ctx, "POST", "/v1/auth/start", authStartRequest{
		Backend: backend, Executable: command.Executable,
	}, &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	return &remoteAuthProcess{
		client: l.client, id: response.ID, url: response.URL,
		code: response.UserCode, wantsInput: response.WantsInput,
		cancelled: make(chan struct{}),
	}, nil
}

type remoteAuthProcess struct {
	client     *Client
	id         string
	url        string
	code       string
	wantsInput bool
	once       sync.Once
	cancelled  chan struct{}
}

func (p *remoteAuthProcess) Challenge() (string, string, bool) {
	return p.url, p.code, p.wantsInput
}

func (p *remoteAuthProcess) Submit(ctx context.Context, code string) error {
	var response authStatusResponse
	if err := p.client.call(ctx, "POST", "/v1/auth/submit", authFlowRequest{
		ID: p.id, Code: code,
	}, &response); err != nil {
		return err
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

func (p *remoteAuthProcess) Wait() (bool, string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.cancelled:
			return false, "runner authentication cancelled"
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var response authStatusResponse
		err := p.client.call(ctx, "POST", "/v1/auth/status", authFlowRequest{ID: p.id}, &response)
		cancel()
		if err != nil {
			continue
		}
		if response.Error != "" {
			return false, response.Error
		}
		if response.Done {
			ok, detail := response.OK, response.Detail
			_ = p.Cancel()
			return ok, detail
		}
	}
}

func (p *remoteAuthProcess) Cancel() error {
	var result error
	p.once.Do(func() {
		close(p.cancelled)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var response authStatusResponse
		result = p.client.call(ctx, "POST", "/v1/auth/cancel", authFlowRequest{ID: p.id}, &response)
		if result == nil && response.Error != "" {
			result = errors.New(response.Error)
		}
	})
	return result
}

var _ providerauth.Launcher = (*AuthLauncher)(nil)
