package providerauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	defaultFlowTTL = 15 * time.Minute
	maxCodeBytes   = 4096
)

type Authorizer interface {
	AuthorizeProviderAuth(context.Context, int64, string, string) error
}

type Process interface {
	Challenge() (url, userCode string, wantsInput bool)
	Submit(context.Context, string) error
	Wait() (bool, string)
	Cancel() error
}

type Launcher interface {
	Launch(context.Context, string, string) (Process, error)
}

type flow struct {
	actorID   int64
	nodeID    string
	backend   string
	expiresAt time.Time
	process   Process
	state     State
	detail    string
	url       string
	userCode  string
}

type Manager struct {
	nodeID     string
	authorizer Authorizer
	launcher   Launcher
	ttl        time.Duration

	mu    sync.Mutex
	flows map[string]*flow
}

func NewManager(nodeID string, authorizer Authorizer, launcher Launcher) (*Manager, error) {
	if strings.TrimSpace(nodeID) == "" || authorizer == nil || launcher == nil {
		return nil, errors.New("provider authentication dependencies are required")
	}
	return &Manager{
		nodeID: nodeID, authorizer: authorizer, launcher: launcher,
		ttl: defaultFlowTTL, flows: make(map[string]*flow),
	}, nil
}

func (m *Manager) Start(ctx context.Context, request StartRequest) (Status, error) {
	request, err := normalizeStart(request)
	if err != nil || request.NodeID != m.nodeID {
		if err == nil {
			err = ErrFlowNotFound
		}
		return Status{}, err
	}
	if err := m.authorizer.AuthorizeProviderAuth(
		ctx, request.ActorID, request.NodeID, request.Backend,
	); err != nil {
		return Status{}, err
	}
	process, err := m.launcher.Launch(ctx, request.Backend, request.NodeID)
	if err != nil {
		return Status{}, err
	}
	flowID, err := randomFlowID()
	if err != nil {
		_ = process.Cancel()
		return Status{}, err
	}
	url, code, wantsInput := process.Challenge()
	entry := &flow{
		actorID: request.ActorID, nodeID: request.NodeID, backend: request.Backend,
		expiresAt: time.Now().Add(m.ttl), process: process,
		state: StateWaitingUser, url: url, userCode: code,
	}
	if wantsInput {
		entry.state = StateWaitingInput
	}
	m.mu.Lock()
	m.cancelMatchingLocked(request.ActorID, request.Backend)
	m.flows[flowID] = entry
	status := statusOf(flowID, entry)
	m.mu.Unlock()
	go m.observe(flowID, entry)
	go m.expire(flowID, entry)
	return status, nil
}

func (m *Manager) Submit(ctx context.Context, request SubmitRequest) (Status, error) {
	if err := validateFlowRequest(request.ActorID, request.NodeID, request.FlowID); err != nil {
		return Status{}, err
	}
	code := strings.TrimSpace(request.Code)
	if code == "" || len(code) > maxCodeBytes || strings.ContainsRune(code, '\x00') {
		return Status{}, errors.New("invalid provider authorization code")
	}
	m.mu.Lock()
	entry, ok := m.authorizedFlowLocked(request.ActorID, request.NodeID, request.FlowID)
	if !ok {
		m.mu.Unlock()
		return Status{}, ErrFlowNotFound
	}
	backend := entry.backend
	m.mu.Unlock()
	if err := m.authorizer.AuthorizeProviderAuth(
		ctx, request.ActorID, request.NodeID, backend,
	); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	entry, ok = m.authorizedFlowLocked(request.ActorID, request.NodeID, request.FlowID)
	if !ok {
		m.mu.Unlock()
		return Status{}, ErrFlowNotFound
	}
	if entry.state != StateWaitingInput {
		m.mu.Unlock()
		return Status{}, ErrFlowNotWaiting
	}
	entry.state = StateWaitingUser
	m.mu.Unlock()
	if err := entry.process.Submit(ctx, code); err != nil {
		m.mu.Lock()
		entry.state, entry.detail = StateFailed, "provider rejected the authorization code"
		status := statusOf(request.FlowID, entry)
		m.mu.Unlock()
		_ = entry.process.Cancel()
		return status, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return statusOf(request.FlowID, entry), nil
}

func (m *Manager) Status(ctx context.Context, request FlowRequest) (Status, error) {
	if err := validateFlowRequest(request.ActorID, request.NodeID, request.FlowID); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	entry, ok := m.authorizedFlowLocked(request.ActorID, request.NodeID, request.FlowID)
	if !ok {
		m.mu.Unlock()
		return Status{}, ErrFlowNotFound
	}
	backend := entry.backend
	m.mu.Unlock()
	if err := m.authorizer.AuthorizeProviderAuth(
		ctx, request.ActorID, request.NodeID, backend,
	); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok = m.authorizedFlowLocked(request.ActorID, request.NodeID, request.FlowID)
	if !ok {
		return Status{}, ErrFlowNotFound
	}
	return statusOf(request.FlowID, entry), nil
}

func (m *Manager) Cancel(ctx context.Context, request FlowRequest) error {
	if err := validateFlowRequest(request.ActorID, request.NodeID, request.FlowID); err != nil {
		return err
	}
	m.mu.Lock()
	entry, ok := m.authorizedFlowLocked(request.ActorID, request.NodeID, request.FlowID)
	if !ok {
		m.mu.Unlock()
		return ErrFlowNotFound
	}
	backend := entry.backend
	m.mu.Unlock()
	if err := m.authorizer.AuthorizeProviderAuth(
		ctx, request.ActorID, request.NodeID, backend,
	); err != nil {
		return err
	}
	m.mu.Lock()
	entry, ok = m.authorizedFlowLocked(request.ActorID, request.NodeID, request.FlowID)
	if !ok {
		m.mu.Unlock()
		return ErrFlowNotFound
	}
	entry.state = StateCancelled
	delete(m.flows, request.FlowID)
	m.mu.Unlock()
	return entry.process.Cancel()
}

func (m *Manager) Close() error {
	m.mu.Lock()
	flows := make([]*flow, 0, len(m.flows))
	for id, entry := range m.flows {
		entry.state = StateCancelled
		flows = append(flows, entry)
		delete(m.flows, id)
	}
	m.mu.Unlock()
	var result error
	for _, entry := range flows {
		if err := entry.process.Cancel(); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (m *Manager) observe(flowID string, entry *flow) {
	ok, _ := entry.process.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.flows[flowID]
	if !exists || current != entry || entry.state.Terminal() {
		return
	}
	if ok {
		entry.state, entry.detail = StateSucceeded, ""
	} else {
		entry.state, entry.detail = StateFailed, "provider did not complete authentication"
	}
}

func (m *Manager) expire(flowID string, entry *flow) {
	timer := time.NewTimer(time.Until(entry.expiresAt))
	defer timer.Stop()
	<-timer.C
	m.mu.Lock()
	current, exists := m.flows[flowID]
	if !exists || current != entry {
		m.mu.Unlock()
		return
	}
	terminal := entry.state.Terminal()
	if !terminal {
		entry.state = StateCancelled
	}
	delete(m.flows, flowID)
	m.mu.Unlock()
	if !terminal {
		_ = entry.process.Cancel()
	}
}

func (m *Manager) authorizedFlowLocked(actorID int64, nodeID, flowID string) (*flow, bool) {
	entry, ok := m.flows[flowID]
	if !ok || entry.actorID != actorID || entry.nodeID != nodeID {
		return nil, false
	}
	if !time.Now().Before(entry.expiresAt) {
		delete(m.flows, flowID)
		entry.state = StateCancelled
		go entry.process.Cancel()
		return nil, false
	}
	return entry, true
}

func (m *Manager) cancelMatchingLocked(actorID int64, backend string) {
	for id, entry := range m.flows {
		if entry.actorID == actorID && entry.backend == backend && !entry.state.Terminal() {
			entry.state = StateCancelled
			delete(m.flows, id)
			go entry.process.Cancel()
		}
	}
}

func statusOf(flowID string, entry *flow) Status {
	return Status{
		FlowID: flowID, Backend: entry.backend, State: entry.state,
		URL: entry.url, UserCode: entry.userCode, Detail: entry.detail,
		ExpiresAt: entry.expiresAt,
	}
}

func randomFlowID() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

var _ Service = (*Manager)(nil)
var _ Closer = (*Manager)(nil)
