package promptpreprocesscore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/promptpreprocessbinding"
)

var (
	ErrUnavailable = errors.New("prompt preprocessing satellite is unavailable")
	ErrInvocation  = errors.New("prompt preprocessing satellite invocation failed")
)

// Mode aliases the provider-neutral durable topology type.
type Mode = promptpreprocess.Mode

const (
	ModeDisabled   = promptpreprocess.ModeDisabled
	ModeShared     = promptpreprocess.ModeShared
	ModePerSession = promptpreprocess.ModePerSession
)

// DesiredState follows the primary session lifecycle. It is durable even while
// preprocessing is disabled or shared, so a later per-session mode can start
// every known active primary without reconstructing lifecycle from UI state.
type DesiredState = promptpreprocessbinding.DesiredState

const (
	DesiredActive   = promptpreprocessbinding.DesiredActive
	DesiredArchived = promptpreprocessbinding.DesiredArchived
)

type PrimaryState struct {
	SessionID domain.SessionID
	Desired   DesiredState
}

type BindingKey = promptpreprocessbinding.BindingKey

// Binding is the hidden durable satellite record. ProviderSessionID is empty
// until a provider thread has been created successfully.
type Binding = promptpreprocessbinding.Binding

// BindingStore is deliberately storage-agnostic. Its implementation must make
// each method durable before returning; satellites are never domain.Session
// records and therefore never enter active/archive UI projections.
type BindingStore = promptpreprocessbinding.Store

// Request is the provider-neutral preprocessing request. Its Mode is captured
// durably by the caller and is never replaced with the manager's current mode.
type Request = promptpreprocess.Request

type StartRequest struct {
	Key                     BindingKey
	ResumeProviderSessionID string
}

type Session interface {
	Process(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error)
	Binding() string
	Close(context.Context) error
}

type Factory func(context.Context, StartRequest) (Session, error)

// Manager owns all hidden satellite processes, FIFO lanes and lifecycle
// bindings. One slot has one reader/writer and therefore exactly one turn in
// flight. A successful turn remains the slot's admission owner until its
// durable Completion is accepted, but acceptance never retires the session.
type Manager struct {
	factory Factory
	store   BindingStore

	rootContext context.Context
	cancelRoot  context.CancelFunc

	mu            sync.Mutex
	bindingMu     sync.Mutex
	mode          Mode
	generation    uint64
	topologyDirty bool
	known         map[domain.SessionID]DesiredState
	slots         map[BindingKey]*satelliteSlot
	stale         []*satelliteSlot
	closed        bool
}

func New(factory Factory, store BindingStore) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	if store == nil {
		store = newVolatileBindingStore()
	}
	return &Manager{
		factory: factory, store: store, rootContext: ctx, cancelRoot: cancel,
		mode: ModeShared, known: make(map[domain.SessionID]DesiredState), slots: make(map[BindingKey]*satelliteSlot),
	}
}

func newManager(factory Factory, store BindingStore) *Manager { return New(factory, store) }

func validMode(mode Mode) bool {
	return mode == ModeDisabled || mode == ModeShared || mode == ModePerSession
}

func validDesired(desired DesiredState) bool {
	return desired == DesiredActive || desired == DesiredArchived
}

func validPrimaryID(id domain.SessionID) bool { return strings.TrimSpace(string(id)) != "" }

func (manager *Manager) Mode() Mode {
	if manager == nil {
		return ModeDisabled
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.mode
}

// Warmup eagerly starts the shared lane. It is a no-op in other modes.
func (manager *Manager) Warmup(ctx context.Context) error {
	if manager == nil || ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	manager.mu.Lock()
	if manager.closed || manager.mode != ModeShared {
		manager.mu.Unlock()
		return nil
	}
	slot := manager.slotLocked(BindingKey{Mode: ModeShared})
	manager.mu.Unlock()
	return slot.ensureStarted(ctx)
}

// SetMode changes the admission generation. Old workers are detached before
// new work is admitted. Desired primary state remains intact in every mode.
func (manager *Manager) SetMode(ctx context.Context, mode Mode) error {
	if manager == nil || ctx == nil || ctx.Err() != nil || !validMode(mode) {
		return ErrUnavailable
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return ErrUnavailable
	}
	if manager.mode == mode && !manager.topologyDirty {
		manager.mu.Unlock()
		return nil
	}
	if manager.mode != mode {
		manager.mode = mode
		manager.generation++
	}
	manager.topologyDirty = false
	old := manager.retireCurrentSlotsLocked()
	active := manager.activePrimaryIDsLocked()
	manager.mu.Unlock()

	var result error
	for _, slot := range old {
		result = errors.Join(result, manager.retireSlot(ctx, slot))
	}
	switch mode {
	case ModeDisabled:
		return result
	case ModeShared:
		manager.mu.Lock()
		if !manager.closed && manager.mode == mode {
			slot := manager.slotLocked(BindingKey{Mode: ModeShared})
			manager.mu.Unlock()
			return errors.Join(result, slot.startForTopology(ctx))
		}
		manager.mu.Unlock()
		return errors.Join(result, ErrUnavailable)
	case ModePerSession:
		return errors.Join(result, manager.startPrimaries(ctx, active))
	default:
		return errors.Join(result, ErrUnavailable)
	}
}

// Activate records an active main session in every mode. In per-session mode
// it also starts or resumes its satellite. A start error is returned only after
// desired=active is durable, so the caller never has to roll back the main.
func (manager *Manager) Activate(ctx context.Context, id domain.SessionID) error {
	return manager.setPrimaryDesired(ctx, id, DesiredActive, true)
}

// Restore has the same desired state as Activate but names the archive flow.
func (manager *Manager) Restore(ctx context.Context, id domain.SessionID) error {
	return manager.setPrimaryDesired(ctx, id, DesiredActive, true)
}

// Archive durably records desired=archived before stopping a personal process.
// The provider thread identity remains available for exact Restore.
func (manager *Manager) Archive(ctx context.Context, id domain.SessionID) error {
	if manager == nil || ctx == nil || ctx.Err() != nil || !validPrimaryID(id) {
		return ErrUnavailable
	}
	key := BindingKey{Mode: ModePerSession, SessionID: id}
	if err := manager.saveDesired(ctx, key, DesiredArchived); err != nil {
		return err
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return ErrUnavailable
	}
	manager.known[id] = DesiredArchived
	slots := manager.detachPrimarySlotsLocked(key)
	manager.mu.Unlock()
	var result error
	for _, slot := range slots {
		result = errors.Join(result, slot.Close(ctx))
	}
	return result
}

// Forget handles a proven-empty main close. There is no main archive to resume,
// so the process is stopped and the hidden binding is removed completely.
func (manager *Manager) Forget(ctx context.Context, id domain.SessionID) error {
	if manager == nil || ctx == nil || ctx.Err() != nil || !validPrimaryID(id) {
		return ErrUnavailable
	}
	key := BindingKey{Mode: ModePerSession, SessionID: id}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return ErrUnavailable
	}
	delete(manager.known, id)
	slots := manager.detachPrimarySlotsLocked(key)
	manager.mu.Unlock()
	var result error
	for _, slot := range slots {
		result = errors.Join(result, slot.Close(ctx))
	}
	manager.bindingMu.Lock()
	deleteErr := manager.store.Delete(context.WithoutCancel(ctx), key)
	manager.bindingMu.Unlock()
	return errors.Join(result, deleteErr)
}

// Reconcile installs an authoritative snapshot of primary lifecycle state.
// Repeating the same snapshot neither duplicates workers nor provider starts.
func (manager *Manager) Reconcile(ctx context.Context, states []PrimaryState) error {
	if manager == nil || ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	desired := make(map[domain.SessionID]DesiredState, len(states))
	for _, state := range states {
		if !validPrimaryID(state.SessionID) || !validDesired(state.Desired) {
			return ErrUnavailable
		}
		desired[state.SessionID] = state.Desired
	}
	manager.bindingMu.Lock()
	existing, err := manager.store.List(ctx)
	manager.bindingMu.Unlock()
	if err != nil {
		return err
	}
	var result error
	for id, state := range desired {
		result = errors.Join(result, manager.saveDesired(ctx, BindingKey{Mode: ModePerSession, SessionID: id}, state))
	}
	for _, binding := range existing {
		if binding.Key.Mode != ModePerSession {
			continue
		}
		if _, found := desired[binding.Key.SessionID]; !found {
			manager.bindingMu.Lock()
			deleteErr := manager.store.Delete(context.WithoutCancel(ctx), binding.Key)
			manager.bindingMu.Unlock()
			result = errors.Join(result, deleteErr)
		}
	}
	if result != nil {
		return result
	}

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return ErrUnavailable
	}
	manager.known = desired
	mode := manager.mode
	var closeSlots []*satelliteSlot
	for key, slot := range manager.slots {
		if key.Mode == ModePerSession && desired[key.SessionID] != DesiredActive {
			delete(manager.slots, key)
			closeSlots = append(closeSlots, slot)
		}
	}
	keptStale := manager.stale[:0]
	for _, slot := range manager.stale {
		if slot.key.Mode == ModePerSession && desired[slot.key.SessionID] != DesiredActive {
			closeSlots = append(closeSlots, slot)
			continue
		}
		keptStale = append(keptStale, slot)
	}
	manager.stale = keptStale
	active := manager.activePrimaryIDsLocked()
	manager.mu.Unlock()
	for _, slot := range closeSlots {
		result = errors.Join(result, slot.Close(ctx))
	}
	if mode == ModePerSession {
		result = errors.Join(result, manager.startPrimaries(ctx, active))
	}
	return result
}

func (manager *Manager) Process(ctx context.Context, request Request) (promptpreprocess.Result, error) {
	if manager == nil || ctx == nil || ctx.Err() != nil || !validMode(request.Mode) || request.Mode == ModeDisabled ||
		strings.TrimSpace(request.MessageID) == "" || strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.Text) == "" {
		return promptpreprocess.Result{}, ErrUnavailable
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return promptpreprocess.Result{}, ErrUnavailable
	}
	var key BindingKey
	if request.Mode == ModeShared {
		key = BindingKey{Mode: ModeShared}
	} else {
		if !validPrimaryID(request.SessionID) || manager.known[request.SessionID] == DesiredArchived {
			manager.mu.Unlock()
			return promptpreprocess.Result{}, ErrUnavailable
		}
		manager.known[request.SessionID] = DesiredActive
		key = BindingKey{Mode: ModePerSession, SessionID: request.SessionID}
	}
	slot := manager.matchingStaleSlotLocked(key, request)
	if slot == nil {
		if manager.mode == request.Mode {
			slot = manager.slotLocked(key)
		} else {
			slot = manager.staleSlotLocked(key)
			if slot == nil {
				slot = newSatelliteSlot(manager, manager.rootContext, manager.factory, manager.store, &manager.bindingMu, key, manager.generation, nil)
				slot.markRetiring()
				manager.stale = append(manager.stale, slot)
			}
		}
	}
	manager.mu.Unlock()
	if key.Mode == ModePerSession {
		if err := manager.saveDesired(ctx, key, DesiredActive); err != nil {
			return promptpreprocess.Result{}, err
		}
	}
	return slot.Process(ctx, request)
}

func (manager *Manager) Invalidate() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return
	}
	manager.generation++
	manager.topologyDirty = true
	old := manager.retireCurrentSlotsLocked()
	manager.mu.Unlock()
	for _, slot := range old {
		closeCtx, cancel := closeContext()
		_ = manager.retireSlot(closeCtx, slot)
		cancel()
	}
}

func (manager *Manager) Close(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closed = true
	manager.cancelRoot()
	old := manager.detachAllSlotsLocked()
	manager.mu.Unlock()
	var result error
	for _, slot := range old {
		result = errors.Join(result, slot.Close(ctx))
	}
	return result
}

func (manager *Manager) setPrimaryDesired(ctx context.Context, id domain.SessionID, desired DesiredState, eager bool) error {
	if manager == nil || ctx == nil || ctx.Err() != nil || !validPrimaryID(id) || !validDesired(desired) {
		return ErrUnavailable
	}
	key := BindingKey{Mode: ModePerSession, SessionID: id}
	if err := manager.saveDesired(ctx, key, desired); err != nil {
		return err
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return ErrUnavailable
	}
	manager.known[id] = desired
	mode := manager.mode
	var slot *satelliteSlot
	if eager && desired == DesiredActive && mode == ModePerSession {
		slot = manager.slotLocked(key)
	}
	manager.mu.Unlock()
	if slot != nil {
		return slot.startForTopology(ctx)
	}
	return nil
}

func (manager *Manager) saveDesired(ctx context.Context, key BindingKey, desired DesiredState) error {
	manager.bindingMu.Lock()
	defer manager.bindingMu.Unlock()
	binding, found, err := manager.store.Load(ctx, key)
	if err != nil {
		return err
	}
	if !found {
		binding = Binding{Key: key}
	}
	binding.Key = key
	binding.Desired = desired
	return manager.store.Save(context.WithoutCancel(ctx), binding)
}

func (manager *Manager) startPrimaries(ctx context.Context, ids []domain.SessionID) error {
	type outcome struct{ err error }
	outcomes := make(chan outcome, len(ids))
	for _, id := range ids {
		manager.mu.Lock()
		if manager.closed || manager.mode != ModePerSession {
			manager.mu.Unlock()
			outcomes <- outcome{err: ErrUnavailable}
			continue
		}
		slot := manager.slotLocked(BindingKey{Mode: ModePerSession, SessionID: id})
		manager.mu.Unlock()
		go func(slot *satelliteSlot) { outcomes <- outcome{err: slot.startForTopology(ctx)} }(slot)
	}
	var result error
	for range ids {
		result = errors.Join(result, (<-outcomes).err)
	}
	return result
}

func (manager *Manager) activePrimaryIDsLocked() []domain.SessionID {
	ids := make([]domain.SessionID, 0, len(manager.known))
	for id, desired := range manager.known {
		if desired == DesiredActive {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (manager *Manager) slotLocked(key BindingKey) *satelliteSlot {
	if current := manager.slots[key]; current != nil {
		return current
	}
	var predecessor <-chan struct{}
	if stale := manager.staleSlotLocked(key); stale != nil {
		predecessor = stale.retired
	}
	current := newSatelliteSlot(manager, manager.rootContext, manager.factory, manager.store, &manager.bindingMu, key, manager.generation, predecessor)
	manager.slots[key] = current
	return current
}

func (manager *Manager) detachSlotLocked(key BindingKey) *satelliteSlot {
	current := manager.slots[key]
	delete(manager.slots, key)
	return current
}

func (manager *Manager) detachPrimarySlotsLocked(key BindingKey) []*satelliteSlot {
	var result []*satelliteSlot
	if current := manager.detachSlotLocked(key); current != nil {
		result = append(result, current)
	}
	kept := manager.stale[:0]
	for _, current := range manager.stale {
		if current.key == key {
			result = append(result, current)
			continue
		}
		kept = append(kept, current)
	}
	manager.stale = kept
	return result
}

func (manager *Manager) detachAllSlotsLocked() []*satelliteSlot {
	result := make([]*satelliteSlot, 0, len(manager.slots)+len(manager.stale))
	for key, current := range manager.slots {
		delete(manager.slots, key)
		result = append(result, current)
	}
	result = append(result, manager.stale...)
	manager.stale = nil
	return result
}

func (manager *Manager) retireCurrentSlotsLocked() []*satelliteSlot {
	var closeNow []*satelliteSlot
	for key, current := range manager.slots {
		delete(manager.slots, key)
		manager.stale = append(manager.stale, current)
		if current.markRetiring() {
			closeNow = append(closeNow, current)
		}
	}
	return closeNow
}

func (manager *Manager) staleSlotLocked(key BindingKey) *satelliteSlot {
	for index := len(manager.stale) - 1; index >= 0; index-- {
		if manager.stale[index].key == key {
			return manager.stale[index]
		}
	}
	return nil
}

func (manager *Manager) matchingStaleSlotLocked(key BindingKey, request promptpreprocess.Request) *satelliteSlot {
	for index := len(manager.stale) - 1; index >= 0; index-- {
		current := manager.stale[index]
		if current.key == key && current.matches(request) {
			return current
		}
	}
	return nil
}

func (manager *Manager) retireSlot(ctx context.Context, target *satelliteSlot) error {
	closeErr := target.Close(ctx)
	manager.mu.Lock()
	for index, current := range manager.stale {
		if current != target {
			continue
		}
		manager.stale = append(manager.stale[:index], manager.stale[index+1:]...)
		break
	}
	if manager.slots[target.key] == target {
		delete(manager.slots, target.key)
	}
	manager.mu.Unlock()
	return closeErr
}

type volatileBindingStore struct {
	mu      sync.Mutex
	records map[BindingKey]Binding
}

func newVolatileBindingStore() *volatileBindingStore {
	return &volatileBindingStore{records: make(map[BindingKey]Binding)}
}

func (store *volatileBindingStore) Load(_ context.Context, key BindingKey) (Binding, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[key]
	return record, found, nil
}

func (store *volatileBindingStore) Save(_ context.Context, binding Binding) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.records[binding.Key] = binding
	return nil
}

func (store *volatileBindingStore) List(context.Context) ([]Binding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]Binding, 0, len(store.records))
	for _, record := range store.records {
		result = append(result, record)
	}
	return result, nil
}

func (store *volatileBindingStore) Delete(_ context.Context, key BindingKey) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.records, key)
	return nil
}
