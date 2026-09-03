// Package updatecomposition binds explicit update triggers to the durable
// signed-release flow without selecting an external release endpoint.
package updatecomposition

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"bria/internal/update"
	"bria/internal/updateflow"
	"bria/internal/updateinstall"
)

var (
	ErrInvalidComposition = errors.New("invalid update runtime composition")
	ErrTriggerRunning     = errors.New("update runtime trigger is already running")
	ErrScheduleDisabled   = errors.New("update runtime schedule is disabled")
)

// FlowService is the deliberately narrow durable update boundary. It makes
// manual starts and reopen recovery visible without giving composition access
// to staging or installer internals.
type FlowService interface {
	Start(context.Context, updateflow.Request) (updateflow.State, error)
	Run(context.Context, string) (updateflow.State, error)
}

// TargetSource provides the currently observed update population. The source
// must not claim an offline or unknown node was updated; updateflow records
// such nodes as blocked before it executes an operation.
type TargetSource interface {
	Targets(context.Context) ([]Target, error)
}

// StateFingerprintProducer reads the state identity that must remain exact
// across both installation and rollback. It is sampled for every fresh flow,
// never reconstructed from a release artifact.
type StateFingerprintProducer interface {
	StateFingerprint(context.Context, string) (string, error)
}

// InstalledStateFingerprints adapts the exact state rereader used by the
// packaged installer and postflight. This prevents a fresh flow from hashing
// a different notion of state than the one later required for rollback.
type InstalledStateFingerprints struct {
	Reader updateinstall.InstallStateReader
}

func (p InstalledStateFingerprints) StateFingerprint(ctx context.Context, nodeID string) (string, error) {
	if nilInterface(p.Reader) || ctx == nil || !validIdentity(nodeID, 1024) {
		return "", ErrInvalidComposition
	}
	state, err := p.Reader.ReadInstalledState(ctx, nodeID)
	if err != nil {
		return "", err
	}
	if !validIdentity(state.StateFingerprint, 4096) {
		return "", ErrInvalidComposition
	}
	return state.StateFingerprint, nil
}

// FlowIDSource creates a new durable identity only for a scheduled flow. A
// manually triggered flow always receives its identity from the caller.
type FlowIDSource interface {
	NextFlowID(context.Context) (string, error)
}

type Target struct {
	Node     update.Node
	Platform string
	Arch     string
}

// StaticTargetSource is useful for a single-machine runtime and for an
// explicitly configured connected set. It returns a copy on every call so a
// caller cannot mutate future update population through an earlier result.
type StaticTargetSource struct{ Values []Target }

func (s StaticTargetSource) Targets(context.Context) ([]Target, error) {
	return append([]Target(nil), s.Values...), nil
}

// Schedule is disabled when Interval is zero. Automatic work is therefore
// opt-in; a runtime can still expose Trigger for an explicit user action.
type Schedule struct{ Interval time.Duration }

type Config struct {
	Service      FlowService
	Targets      TargetSource
	Fingerprints StateFingerprintProducer
	Schedule     Schedule
	FlowIDs      FlowIDSource
}

type scheduledResult struct {
	state updateflow.State
	err   error
}

// Composition owns only trigger selection, exact target capture and recovery.
// Source signature verification, artifact selection, staging and rollback stay
// inside updateruntime/updateflow and are never duplicated here.
type Composition struct {
	service      FlowService
	targets      TargetSource
	fingerprints StateFingerprintProducer
	schedule     Schedule
	flowIDs      FlowIDSource

	mu      sync.Mutex
	running chan scheduledResult
}

func Open(config Config) (*Composition, error) {
	if nilInterface(config.Service) || nilInterface(config.Targets) || nilInterface(config.Fingerprints) ||
		config.Schedule.Interval < 0 || (config.Schedule.Interval > 0 && nilInterface(config.FlowIDs)) {
		return nil, ErrInvalidComposition
	}
	return &Composition{
		service: config.Service, targets: config.Targets, fingerprints: config.Fingerprints,
		schedule: config.Schedule, flowIDs: config.FlowIDs,
	}, nil
}

// Trigger begins one caller-named update flow after sampling the exact state
// fingerprint of every selected node. The selected signed artifact remains
// immutable inside updateflow's durable state after this call returns.
func (c *Composition) Trigger(ctx context.Context, flowID string) (updateflow.State, error) {
	if c == nil || ctx == nil || !validIdentity(flowID, 1024) {
		return updateflow.State{}, ErrInvalidComposition
	}
	request, err := c.request(ctx, flowID)
	if err != nil {
		return updateflow.State{}, err
	}
	return c.service.Start(ctx, request)
}

// Reopen continues exactly one known durable flow. It does not fetch a new
// manifest, resample state, or create another flow identity.
func (c *Composition) Reopen(ctx context.Context, flowID string) (updateflow.State, error) {
	if c == nil || ctx == nil || !validIdentity(flowID, 1024) {
		return updateflow.State{}, ErrInvalidComposition
	}
	return c.service.Run(ctx, flowID)
}

// TriggerScheduled starts a single asynchronous scheduled flow. It never
// overlaps a running trigger; callers use Wait to reread its durable outcome.
func (c *Composition) TriggerScheduled(ctx context.Context) (updateflow.State, error) {
	if c == nil || ctx == nil || c.schedule.Interval <= 0 || nilInterface(c.flowIDs) {
		return updateflow.State{}, ErrScheduleDisabled
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running != nil {
		return updateflow.State{}, ErrTriggerRunning
	}
	flowID, err := c.flowIDs.NextFlowID(ctx)
	if err != nil {
		return updateflow.State{}, err
	}
	if !validIdentity(flowID, 1024) {
		return updateflow.State{}, ErrInvalidComposition
	}
	done := make(chan scheduledResult, 1)
	c.running = done
	go func() {
		state, triggerErr := c.Trigger(ctx, flowID)
		done <- scheduledResult{state: state, err: triggerErr}
	}()
	return updateflow.State{FlowID: flowID}, nil
}

// Wait returns the durable outcome of the current scheduled trigger. A caller
// must explicitly invoke Reopen if process recovery discovers an old flow.
func (c *Composition) Wait(ctx context.Context) (updateflow.State, error) {
	if c == nil || ctx == nil {
		return updateflow.State{}, ErrInvalidComposition
	}
	c.mu.Lock()
	done := c.running
	c.mu.Unlock()
	if done == nil {
		return updateflow.State{}, ErrScheduleDisabled
	}
	select {
	case result := <-done:
		c.mu.Lock()
		if c.running == done {
			c.running = nil
		}
		c.mu.Unlock()
		return result.state, result.err
	case <-ctx.Done():
		return updateflow.State{}, ctx.Err()
	}
}

// Run waits for explicitly configured schedule ticks. It intentionally does
// not update at startup, and it stops on the first terminal trigger outcome.
func (c *Composition) Run(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrInvalidComposition
	}
	if c.schedule.Interval <= 0 {
		return ErrScheduleDisabled
	}
	ticker := time.NewTicker(c.schedule.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := c.TriggerScheduled(ctx); err != nil {
				if errors.Is(err, ErrTriggerRunning) {
					continue
				}
				return err
			}
			if _, err := c.Wait(ctx); err != nil {
				return err
			}
		}
	}
}

func (c *Composition) request(ctx context.Context, flowID string) (updateflow.Request, error) {
	targets, err := c.targets.Targets(ctx)
	if err != nil {
		return updateflow.Request{}, err
	}
	if len(targets) == 0 {
		return updateflow.Request{}, ErrInvalidComposition
	}
	seen := make(map[string]struct{}, len(targets))
	coordinators := 0
	result := make([]updateflow.Target, 0, len(targets))
	for _, target := range targets {
		if !validIdentity(target.Node.ID, 1024) || !validIdentity(target.Node.CurrentVersion, 128) ||
			!validIdentity(target.Platform, 64) || !validIdentity(target.Arch, 64) {
			return updateflow.Request{}, ErrInvalidComposition
		}
		if _, duplicate := seen[target.Node.ID]; duplicate {
			return updateflow.Request{}, ErrInvalidComposition
		}
		seen[target.Node.ID] = struct{}{}
		switch target.Node.Role {
		case update.RoleExecutor:
		case update.RoleCoordinator:
			coordinators++
		default:
			return updateflow.Request{}, ErrInvalidComposition
		}
		switch target.Node.Availability {
		case update.AvailabilityOnline, update.AvailabilityOffline, update.AvailabilityUnknown:
		default:
			return updateflow.Request{}, ErrInvalidComposition
		}
		fingerprint, fingerprintErr := c.fingerprints.StateFingerprint(ctx, target.Node.ID)
		if fingerprintErr != nil {
			return updateflow.Request{}, fingerprintErr
		}
		if !validIdentity(fingerprint, 4096) {
			return updateflow.Request{}, ErrInvalidComposition
		}
		result = append(result, updateflow.Target{Node: target.Node, Platform: target.Platform, Arch: target.Arch, PriorState: fingerprint})
	}
	if coordinators != 1 {
		return updateflow.Request{}, ErrInvalidComposition
	}
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].Node.Role == update.RoleExecutor && result[right].Node.Role == update.RoleCoordinator
	})
	return updateflow.Request{FlowID: flowID, Targets: result}, nil
}

func validIdentity(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflection := reflect.ValueOf(value)
	return (reflection.Kind() == reflect.Chan || reflection.Kind() == reflect.Func || reflection.Kind() == reflect.Interface ||
		reflection.Kind() == reflect.Map || reflection.Kind() == reflect.Pointer || reflection.Kind() == reflect.Slice) && reflection.IsNil()
}
