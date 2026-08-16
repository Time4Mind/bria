package backendsetup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/systemdeps"
)

const installTimeout = 15 * time.Minute

type Config struct {
	NodeID       string
	Roots        map[string]string
	Commands     map[string]string
	Runner       runtimehost.CommandRunner
	Refresh      func(context.Context)
	Dependencies systemdeps.Config
}

type Manager struct {
	config Config
	mu     sync.Mutex
	status map[string]Status
}

func NewManager(config Config) (*Manager, error) {
	if strings.TrimSpace(config.NodeID) == "" || config.Runner == nil {
		return nil, errors.New("backend setup node and runner are required")
	}
	for _, backend := range []string{"claude", "codex"} {
		if !filepath.IsAbs(config.Roots[backend]) || strings.TrimSpace(config.Commands[backend]) == "" {
			return nil, fmt.Errorf("managed %s configuration is invalid", backend)
		}
	}
	return &Manager{config: config, status: make(map[string]Status)}, nil
}

func (m *Manager) Start(_ context.Context, request Request) (Status, error) {
	request, err := m.normalize(request)
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	current := m.currentLocked(request.Backend)
	if current.Phase == PhaseInstalling || current.Phase == PhaseReady {
		m.mu.Unlock()
		return current, nil
	}
	current = Status{
		NodeID: m.config.NodeID, Backend: request.Backend, Phase: PhaseInstalling,
		Detail: "installing provider CLI", UpdatedAt: time.Now(),
	}
	m.status[request.Backend] = current
	m.mu.Unlock()
	go m.install(request.Backend)
	return current, nil
}

func (m *Manager) Status(_ context.Context, request Request) (Status, error) {
	request, err := m.normalize(request)
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.currentLocked(request.Backend)
	if current.Phase == PhaseMissing {
		current = m.inspect(request.Backend)
		m.status[request.Backend] = current
	}
	return current, nil
}

func (m *Manager) install(backend string) {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	if _, err := m.config.Runner.LookPath("npm"); err != nil {
		err = systemdeps.Ensure(ctx, m.config.Dependencies, systemdeps.ProfileNodeJS, func() bool {
			_, lookupErr := m.config.Runner.LookPath("npm")
			return lookupErr == nil
		})
		if err != nil {
			m.finishInstall(backend, fmt.Errorf("install Node.js/npm: %w", err))
			return
		}
	}
	packageName := map[string]string{
		"claude": "@anthropic-ai/claude-code@latest",
		"codex":  "@openai/codex@latest",
	}[backend]
	result, err := m.config.Runner.Run(
		ctx, "npm", "install", "--prefix", m.config.Roots[backend],
		"--no-audit", "--no-fund", "--save=false", packageName,
	)
	if err == nil && result.ExitCode != 0 {
		err = fmt.Errorf("npm exited with %d: %s", result.ExitCode, setupDetail(result.Stderr))
	}
	m.finishInstall(backend, err)
}

func (m *Manager) finishInstall(backend string, err error) {
	status := m.inspect(backend)
	if err != nil {
		status.Phase, status.Detail = PhaseFailed, setupDetail([]byte(err.Error()))
	} else if status.Phase != PhaseReady {
		status.Phase, status.Detail = PhaseFailed, "installed command did not pass its version probe"
	} else if m.config.Refresh != nil {
		m.config.Refresh(context.Background())
	}
	status.UpdatedAt = time.Now()
	m.mu.Lock()
	m.status[backend] = status
	m.mu.Unlock()
}

func (m *Manager) currentLocked(backend string) Status {
	if current, ok := m.status[backend]; ok {
		return current
	}
	return m.inspect(backend)
}

func (m *Manager) inspect(backend string) Status {
	status := Status{
		NodeID: m.config.NodeID, Backend: backend, Phase: PhaseMissing, UpdatedAt: time.Now(),
	}
	probe := runtimehost.NewClaudeCommandProbe(m.config.Runner, m.config.Commands[backend], 5*time.Second)
	if backend == "codex" {
		probe = runtimehost.NewCodexCommandProbe(m.config.Runner, m.config.Commands[backend], 5*time.Second)
	}
	if descriptor, err := probe.Probe(context.Background()); err == nil && descriptor.Available {
		status.Phase, status.Detail = PhaseReady, descriptor.Version
	}
	return status
}

func (m *Manager) normalize(request Request) (Request, error) {
	request.Backend = strings.ToLower(strings.TrimSpace(request.Backend))
	if request.NodeID != m.config.NodeID ||
		(request.Backend != "claude" && request.Backend != "codex") {
		return Request{}, errors.New("invalid backend setup request")
	}
	return request, nil
}

func setupDetail(value []byte) string {
	text := strings.Join(strings.Fields(string(value)), " ")
	if len(text) > 300 {
		return text[:300] + "…"
	}
	if text == "" {
		return "backend installation failed"
	}
	return text
}
