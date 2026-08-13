package runnerhost

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/providerauth"
)

type authStartRequest struct {
	Backend    string `json:"backend"`
	Executable string `json:"executable"`
}

type authStartResponse struct {
	ID         string `json:"id,omitempty"`
	URL        string `json:"url,omitempty"`
	UserCode   string `json:"user_code,omitempty"`
	WantsInput bool   `json:"wants_input"`
	Error      string `json:"error,omitempty"`
}

type authFlowRequest struct {
	ID   string `json:"id"`
	Code string `json:"code,omitempty"`
}

type authStatusResponse struct {
	Done   bool   `json:"done"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

type serverAuthFlow struct {
	process providerauth.Process
	done    bool
	ok      bool
	detail  string
}

type authStore struct {
	mu    sync.Mutex
	flows map[string]*serverAuthFlow
}

func newAuthStore() *authStore { return &authStore{flows: make(map[string]*serverAuthFlow)} }

func (s *authStore) start(ctx context.Context, request authStartRequest) (authStartResponse, error) {
	launcher, err := providerauth.NewCommandLauncher(map[string]providerauth.Command{
		request.Backend: {Executable: request.Executable},
	})
	if err != nil {
		return authStartResponse{}, err
	}
	process, err := launcher.Launch(ctx, request.Backend, "runner")
	if err != nil {
		return authStartResponse{}, err
	}
	id, err := randomAuthID()
	if err != nil {
		_ = process.Cancel()
		return authStartResponse{}, err
	}
	url, code, wantsInput := process.Challenge()
	flow := &serverAuthFlow{process: process}
	s.mu.Lock()
	s.flows[id] = flow
	s.mu.Unlock()
	go func() {
		ok, detail := process.Wait()
		s.mu.Lock()
		flow.done, flow.ok, flow.detail = true, ok, detail
		s.mu.Unlock()
	}()
	go func() {
		timer := time.NewTimer(20 * time.Minute)
		defer timer.Stop()
		<-timer.C
		s.mu.Lock()
		current := s.flows[id]
		s.mu.Unlock()
		if current == flow {
			_ = s.cancel(id)
		}
	}()
	return authStartResponse{ID: id, URL: url, UserCode: code, WantsInput: wantsInput}, nil
}

func (s *authStore) submit(ctx context.Context, request authFlowRequest) error {
	s.mu.Lock()
	flow := s.flows[request.ID]
	s.mu.Unlock()
	if flow == nil {
		return errors.New("runner authentication flow not found")
	}
	return flow.process.Submit(ctx, request.Code)
}

func (s *authStore) status(id string) (authStatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow := s.flows[id]
	if flow == nil {
		return authStatusResponse{}, errors.New("runner authentication flow not found")
	}
	return authStatusResponse{Done: flow.done, OK: flow.ok}, nil
}

func (s *authStore) cancel(id string) error {
	s.mu.Lock()
	flow := s.flows[id]
	delete(s.flows, id)
	s.mu.Unlock()
	if flow == nil {
		return nil
	}
	return flow.process.Cancel()
}

func (s *authStore) close() error {
	s.mu.Lock()
	flows := s.flows
	s.flows = make(map[string]*serverAuthFlow)
	s.mu.Unlock()
	var result error
	for _, flow := range flows {
		result = errors.Join(result, flow.process.Cancel())
	}
	return result
}

func randomAuthID() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
