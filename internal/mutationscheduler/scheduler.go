package mutationscheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	mutationStateVersion = 1
	globalMutationLimit  = 25
	regularMutationLimit = globalMutationLimit - 2
	interactiveLimit     = globalMutationLimit - 1
	globalStartInterval  = time.Second / globalMutationLimit
	baseMutationInterval = 1500 * time.Millisecond
	maxCooldownJitter    = 250 * time.Millisecond
)

type Priority uint8

const (
	Routine Priority = iota
	Interactive
)

// Mutation identifies only scheduling metadata. It never contains Telegram
// text, callback payloads, tokens, or upload content.
type Mutation struct {
	Method   string
	ChatID   int64
	CardID   int64
	Priority Priority
	Heavy    bool
}

type Options struct {
	StatePath string
	Now       func() time.Time
	Wait      func(context.Context, time.Duration) error
	Jitter    func(time.Duration) time.Duration
	Report    func(Diagnostic)
}

// Diagnostic is bounded operational metadata. It deliberately excludes
// Telegram identifiers, payloads, callback data, and credentials.
type Diagnostic struct {
	Method        string
	ErrorClass    string
	RetryAfter    time.Duration
	CooldownUntil time.Time
	QueueSize     int
	OldestAge     time.Duration
}

type mutationDiskState struct {
	Version        int             `json:"version"`
	CooldownUntil  time.Time       `json:"cooldown_until,omitempty"`
	CooldownCause  string          `json:"cooldown_cause,omitempty"`
	LastMutationAt time.Time       `json:"last_mutation_at,omitempty"`
	ServerFailures int             `json:"server_failures,omitempty"`
	ProbeRequired  bool            `json:"probe_required,omitempty"`
	BlockedAll     bool            `json:"blocked_all,omitempty"`
	BlockedChats   map[string]bool `json:"blocked_chats,omitempty"`
}

type cadenceState struct {
	cardID    int64
	count     int
	startedAt time.Time
	lastAt    time.Time
}

type globalStart struct {
	at       time.Time
	priority Priority
}

type waiter struct{ queuedAt time.Time }

type MutationScheduler struct {
	mu            sync.Mutex
	path          string
	now           func() time.Time
	wait          func(context.Context, time.Duration) error
	jitter        func(time.Duration) time.Duration
	report        func(Diagnostic)
	state         mutationDiskState
	globalStarts  []globalStart
	cadence       map[int64]cadenceState
	heavy         chan struct{}
	probeInFlight bool
	notify        chan struct{}
	epoch         uint64
	cardVersions  map[string]uint64
	waiters       map[uint64]waiter
	nextWaiterID  uint64
}

type Lease struct {
	scheduler *MutationScheduler
	mutation  Mutation
	heavy     bool
	probe     bool
	epoch     uint64
	once      sync.Once
}

var ErrStopped = errors.New("Telegram mutations are stopped")
var ErrSuperseded = errors.New("Telegram card mutation was superseded")

type Outcome struct {
	HTTPStatus int
	ErrorCode  int
	RetryAfter time.Duration
	Err        error
}

func Open(options Options) (*MutationScheduler, error) {
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	wait := options.Wait
	if wait == nil {
		wait = waitDuration
	}
	jitter := options.Jitter
	if jitter == nil {
		jitter = func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return time.Duration(now().UnixNano() % int64(max+1))
		}
	}
	scheduler := &MutationScheduler{
		path: options.StatePath, now: now, wait: wait, jitter: jitter, report: options.Report,
		state:   mutationDiskState{Version: mutationStateVersion, BlockedChats: make(map[string]bool)},
		cadence: make(map[int64]cadenceState), heavy: make(chan struct{}, 1), notify: make(chan struct{}),
		cardVersions: make(map[string]uint64), waiters: make(map[uint64]waiter),
	}
	if options.StatePath != "" {
		absolute, err := filepath.Abs(options.StatePath)
		if err != nil {
			return nil, fmt.Errorf("resolve Telegram scheduler state path: %w", err)
		}
		scheduler.path = absolute
		if err := scheduler.load(); err != nil {
			return nil, err
		}
	}
	return scheduler, nil
}

func (scheduler *MutationScheduler) Acquire(ctx context.Context, mutation Mutation) (*Lease, error) {
	if scheduler == nil || ctx == nil || mutation.Method == "" {
		return nil, errors.New("Telegram mutation scheduler request is invalid")
	}
	scheduler.mu.Lock()
	scheduler.nextWaiterID++
	waiterID := scheduler.nextWaiterID
	scheduler.waiters[waiterID] = waiter{queuedAt: scheduler.now().UTC()}
	scheduler.mu.Unlock()
	defer func() {
		scheduler.mu.Lock()
		delete(scheduler.waiters, waiterID)
		scheduler.mu.Unlock()
	}()
	heavy := false
	if mutation.Heavy {
		select {
		case scheduler.heavy <- struct{}{}:
			heavy = true
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	cardKey := mutationCardKey(mutation)
	var cardVersion uint64
	if cardKey != "" {
		scheduler.mu.Lock()
		scheduler.cardVersions[cardKey]++
		cardVersion = scheduler.cardVersions[cardKey]
		scheduler.broadcastLocked()
		scheduler.mu.Unlock()
	}
	releaseHeavy := func() {
		if heavy {
			<-scheduler.heavy
		}
	}
	for {
		scheduler.mu.Lock()
		if cardKey != "" && scheduler.cardVersions[cardKey] != cardVersion {
			scheduler.mu.Unlock()
			releaseHeavy()
			return nil, ErrSuperseded
		}
		now := scheduler.now().UTC()
		if scheduler.state.BlockedAll || (mutation.ChatID != 0 && scheduler.state.BlockedChats[strconv.FormatInt(int64(mutation.ChatID), 10)]) {
			scheduler.mu.Unlock()
			releaseHeavy()
			return nil, ErrStopped
		}
		delay := scheduler.delayLocked(now, mutation)
		probe := scheduler.state.ProbeRequired && mutation.Priority != Interactive
		if probe && scheduler.probeInFlight {
			notify := scheduler.notify
			scheduler.mu.Unlock()
			if err := waitForSchedulerChange(ctx, notify); err != nil {
				releaseHeavy()
				return nil, err
			}
			continue
		}
		if delay <= 0 {
			if probe {
				scheduler.probeInFlight = true
			}
			scheduler.recordStartLocked(now, mutation)
			epoch := scheduler.epoch
			if err := scheduler.persistLocked(); err != nil {
				if probe {
					scheduler.probeInFlight = false
				}
				scheduler.mu.Unlock()
				releaseHeavy()
				return nil, err
			}
			scheduler.broadcastLocked()
			scheduler.mu.Unlock()
			return &Lease{scheduler: scheduler, mutation: mutation, heavy: heavy, probe: probe, epoch: epoch}, nil
		}
		scheduler.mu.Unlock()
		if err := scheduler.wait(ctx, delay); err != nil {
			releaseHeavy()
			return nil, err
		}
	}
}

// ConfirmAuthorization clears only the global 401 stop after the same client
// has successfully authenticated through getMe. Recipient-specific 403 stops
// remain explicit because bot authentication cannot prove a chat is writable.
func (scheduler *MutationScheduler) ConfirmAuthorization() error {
	if scheduler == nil {
		return nil
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if !scheduler.state.BlockedAll {
		return nil
	}
	scheduler.state.BlockedAll = false
	scheduler.broadcastLocked()
	return scheduler.persistLocked()
}

// SetReporter binds a local diagnostic sink. It is safe to call before or
// during runtime; reporting never holds the scheduler lock.
func (scheduler *MutationScheduler) SetReporter(report func(Diagnostic)) {
	if scheduler == nil {
		return
	}
	scheduler.mu.Lock()
	scheduler.report = report
	scheduler.mu.Unlock()
}

func mutationCardKey(mutation Mutation) string {
	if mutation.ChatID == 0 || mutation.CardID <= 0 || mutation.Method != "editMessageText" {
		return ""
	}
	return strconv.FormatInt(int64(mutation.ChatID), 10) + ":" + strconv.FormatInt(int64(mutation.CardID), 10)
}

func (lease *Lease) Complete(outcome Outcome) error {
	if lease == nil || lease.scheduler == nil {
		return nil
	}
	var persistErr error
	lease.once.Do(func() {
		persistErr = lease.scheduler.complete(lease, outcome)
	})
	return persistErr
}

func (scheduler *MutationScheduler) complete(lease *Lease, outcome Outcome) error {
	if lease.heavy {
		<-scheduler.heavy
	}
	scheduler.mu.Lock()
	now := scheduler.now().UTC()
	if lease.probe {
		scheduler.probeInFlight = false
	}
	hasAPIError := outcome.HTTPStatus != 0 || outcome.ErrorCode != 0
	if hasAPIError && isStatus(outcome, 401) {
		scheduler.state.BlockedAll = true
		scheduler.epoch++
	} else if hasAPIError && isStatus(outcome, 403) {
		if lease.mutation.ChatID != 0 {
			scheduler.state.BlockedChats[strconv.FormatInt(int64(lease.mutation.ChatID), 10)] = true
		}
		scheduler.epoch++
	} else if hasAPIError && isStatus(outcome, 429) {
		delay := 4 * time.Second
		if outcome.RetryAfter > delay {
			delay = outcome.RetryAfter
		}
		delay += scheduler.jitter(maxCooldownJitter)
		scheduler.enterCooldownLocked(now, delay, "rate_limited")
	} else if hasAPIError && (outcome.HTTPStatus >= 500 || outcome.ErrorCode >= 500) {
		if scheduler.state.ServerFailures < 4 {
			scheduler.state.ServerFailures++
		}
		delay := serverBackoff(scheduler.state.ServerFailures)
		scheduler.enterCooldownLocked(now, delay, "server_error")
	} else if outcome.Err == nil || hasAPIError {
		// Any definitive non-5xx response proves the transport is reachable.
		if lease.epoch == scheduler.epoch || lease.probe {
			scheduler.state.ServerFailures = 0
			scheduler.state.ProbeRequired = false
			scheduler.state.CooldownUntil = time.Time{}
			scheduler.state.CooldownCause = ""
		}
	} else if !errors.Is(outcome.Err, context.Canceled) {
		// A mutation without a valid protocol response is ambiguous. Back off
		// the shared pool; operation-specific code decides whether retrying the
		// mutation itself is idempotent.
		if scheduler.state.ServerFailures < 4 {
			scheduler.state.ServerFailures++
		}
		scheduler.enterCooldownLocked(now, serverBackoff(scheduler.state.ServerFailures), "transport_error")
	}
	diagnostic := scheduler.diagnosticLocked(now, lease.mutation.Method, outcome)
	scheduler.broadcastLocked()
	persistErr := scheduler.persistLocked()
	report := scheduler.report
	scheduler.mu.Unlock()
	if report != nil && diagnostic.ErrorClass != "" {
		report(diagnostic)
	}
	return persistErr
}

func (scheduler *MutationScheduler) diagnosticLocked(now time.Time, method string, outcome Outcome) Diagnostic {
	class := ""
	switch {
	case isStatus(outcome, 401):
		class = "unauthorized"
	case isStatus(outcome, 403):
		class = "recipient_forbidden"
	case isStatus(outcome, 429):
		class = "rate_limited"
	case outcome.HTTPStatus >= 500 || outcome.ErrorCode >= 500:
		class = "server_error"
	case outcome.Err != nil && !errors.Is(outcome.Err, context.Canceled):
		class = "transport_error"
	case outcome.HTTPStatus >= 400 || outcome.ErrorCode >= 400:
		class = "terminal_api_error"
	}
	oldest := time.Duration(0)
	for _, queued := range scheduler.waiters {
		age := now.Sub(queued.queuedAt)
		if age > oldest {
			oldest = age
		}
	}
	return Diagnostic{Method: method, ErrorClass: class, RetryAfter: outcome.RetryAfter,
		CooldownUntil: scheduler.state.CooldownUntil, QueueSize: len(scheduler.waiters), OldestAge: oldest}
}

func (scheduler *MutationScheduler) enterCooldownLocked(now time.Time, delay time.Duration, cause string) {
	deadline := now.Add(delay)
	if deadline.After(scheduler.state.CooldownUntil) {
		scheduler.state.CooldownUntil = deadline
	}
	scheduler.state.CooldownCause = cause
	scheduler.state.ProbeRequired = true
	scheduler.epoch++
}

func (scheduler *MutationScheduler) delayLocked(now time.Time, mutation Mutation) time.Duration {
	delay := time.Duration(0)
	// Transport cooldowns throttle background work, but must not delay the
	// user's callback card update; global and class windows still apply.
	if mutation.Priority != Interactive {
		delay = scheduler.state.CooldownUntil.Sub(now)
	}
	scheduler.pruneGlobalLocked(now)
	if !scheduler.state.LastMutationAt.IsZero() {
		if spacing := scheduler.state.LastMutationAt.Add(globalStartInterval).Sub(now); spacing > delay {
			delay = spacing
		}
	}
	if len(scheduler.globalStarts) >= globalMutationLimit {
		globalDelay := scheduler.globalStarts[len(scheduler.globalStarts)-globalMutationLimit].at.Add(time.Second).Sub(now)
		if globalDelay > delay {
			delay = globalDelay
		}
	}
	classLimit := regularMutationLimit
	if mutation.Priority == Interactive {
		classLimit = interactiveLimit
	}
	if classDelay := scheduler.classDelayLocked(now, mutation.Priority, classLimit); classDelay > delay {
		delay = classDelay
	}
	if mutation.ChatID != 0 && mutation.CardID > 0 && mutation.Priority != Interactive {
		cadence := scheduler.cadence[mutation.ChatID]
		if mutation.CardID != 0 && cadence.cardID != mutation.CardID {
			cadence = cadenceState{cardID: mutation.CardID}
			scheduler.cadence[mutation.ChatID] = cadence
		}
		chatDelay := cadence.lastAt.Add(cadenceInterval(cadence, now)).Sub(now)
		if chatDelay > delay {
			delay = chatDelay
		}
	}
	if delay < 0 {
		return 0
	}
	return delay
}

func (scheduler *MutationScheduler) recordStartLocked(now time.Time, mutation Mutation) {
	scheduler.globalStarts = append(scheduler.globalStarts, globalStart{at: now, priority: mutation.Priority})
	scheduler.state.LastMutationAt = now
	if mutation.ChatID == 0 || mutation.CardID <= 0 {
		return
	}
	cadence := scheduler.cadence[mutation.ChatID]
	if mutation.CardID != 0 && cadence.cardID != mutation.CardID {
		cadence = cadenceState{cardID: mutation.CardID}
		scheduler.cadence[mutation.ChatID] = cadence
	}
	if mutation.Priority == Interactive {
		return
	}
	if cadence.startedAt.IsZero() {
		cadence.startedAt = now
	}
	cadence.count++
	cadence.lastAt = now
	scheduler.cadence[mutation.ChatID] = cadence
}

func (scheduler *MutationScheduler) pruneGlobalLocked(now time.Time) {
	cutoff := now.Add(-time.Second)
	index := 0
	for index < len(scheduler.globalStarts) && !scheduler.globalStarts[index].at.After(cutoff) {
		index++
	}
	if index > 0 {
		scheduler.globalStarts = append([]globalStart(nil), scheduler.globalStarts[index:]...)
	}
}

func (scheduler *MutationScheduler) classDelayLocked(now time.Time, priority Priority, limit int) time.Duration {
	count := 0
	for index := len(scheduler.globalStarts) - 1; index >= 0; index-- {
		if scheduler.globalStarts[index].priority != priority {
			continue
		}
		count++
		if count == limit {
			return scheduler.globalStarts[index].at.Add(time.Second).Sub(now)
		}
	}
	return 0
}

func cadenceInterval(state cadenceState, now time.Time) time.Duration {
	if !state.startedAt.IsZero() && now.Sub(state.startedAt) >= 30*time.Minute {
		return 5 * time.Second
	}
	if state.count >= 30 {
		return 3500 * time.Millisecond
	}
	if state.count >= 10 {
		return 2500 * time.Millisecond
	}
	return baseMutationInterval
}

func serverBackoff(failures int) time.Duration {
	switch failures {
	case 1:
		return 4 * time.Second
	case 2:
		return 8 * time.Second
	case 3:
		return 12 * time.Second
	default:
		return 15 * time.Second
	}
}

func isStatus(outcome Outcome, status int) bool {
	return outcome.HTTPStatus == status || outcome.ErrorCode == status
}

func waitDuration(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitForSchedulerChange(ctx context.Context, notify <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-notify:
		return nil
	}
}

func (scheduler *MutationScheduler) broadcastLocked() {
	close(scheduler.notify)
	scheduler.notify = make(chan struct{})
}

func (scheduler *MutationScheduler) load() error {
	file, err := os.Open(scheduler.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open Telegram scheduler state: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var state mutationDiskState
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("decode Telegram scheduler state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Telegram scheduler state contains trailing data")
	}
	if state.Version != mutationStateVersion || state.ServerFailures < 0 || state.ServerFailures > 4 {
		return errors.New("Telegram scheduler state is invalid")
	}
	if state.BlockedChats == nil {
		state.BlockedChats = make(map[string]bool)
	}
	scheduler.state = state
	return nil
}

func (scheduler *MutationScheduler) persistLocked() error {
	if scheduler.path == "" {
		return nil
	}
	directory := filepath.Dir(scheduler.path)
	temporary, err := os.CreateTemp(directory, ".telegram-scheduler-*")
	if err != nil {
		return fmt.Errorf("create Telegram scheduler state: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(scheduler.state); err != nil {
		return fmt.Errorf("encode Telegram scheduler state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync Telegram scheduler state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Telegram scheduler state: %w", err)
	}
	if err := os.Rename(temporaryPath, scheduler.path); err != nil {
		return fmt.Errorf("replace Telegram scheduler state: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open Telegram scheduler state directory: %w", err)
	}
	if err := directoryHandle.Sync(); err != nil {
		_ = directoryHandle.Close()
		return fmt.Errorf("sync Telegram scheduler state directory: %w", err)
	}
	if err := directoryHandle.Close(); err != nil {
		return fmt.Errorf("close Telegram scheduler state directory: %w", err)
	}
	keep = true
	return nil
}
