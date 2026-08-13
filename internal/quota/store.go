package quota

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type Collector interface {
	Backend() string
	Collect(context.Context, domain.NodeID) (domain.QuotaSnapshot, error)
}

type StateReader interface {
	State() *domain.State
}

type Store struct {
	nodeID     domain.NodeID
	state      StateReader
	collectors []Collector

	mu        sync.RWMutex
	snapshots map[string]domain.QuotaSnapshot
	daily     map[string]dailyState
}

func NewStore(nodeID domain.NodeID, state StateReader, collectors ...Collector) *Store {
	return &Store{
		nodeID: nodeID, state: state, collectors: append([]Collector(nil), collectors...),
		snapshots: make(map[string]domain.QuotaSnapshot),
		daily:     make(map[string]dailyState),
	}
}

func (s *Store) Snapshots() []domain.QuotaSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.QuotaSnapshot, 0, len(s.snapshots))
	connected := s.connectedBackends()
	for _, snapshot := range s.snapshots {
		if connected[strings.ToLower(snapshot.Backend)] {
			result = append(result, snapshot)
		}
	}
	slices.SortFunc(result, func(a, b domain.QuotaSnapshot) int {
		return cmp.Compare(strings.ToLower(a.Backend), strings.ToLower(b.Backend))
	})
	return result
}

func (s *Store) Run(ctx context.Context) {
	lastRefresh := time.Time{}
	nextScheduled := time.Time{}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state := s.state.State()
		now := time.Now()
		manual := state != nil && state.QuotaRefreshRequestedAt.After(lastRefresh)
		if nextScheduled.IsZero() || manual || !now.Before(nextScheduled) {
			s.collect(ctx)
			if state != nil {
				lastRefresh = state.QuotaRefreshRequestedAt
			}
			nextScheduled = now.Add(time.Duration(quotaPollMinutes(state)) * time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) collect(ctx context.Context) {
	connected := s.connectedBackends()
	var workers sync.WaitGroup
	for _, collector := range s.collectors {
		collector := collector
		if !connected[strings.ToLower(collector.Backend())] {
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			collectCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
			defer cancel()
			snapshot, err := collector.Collect(collectCtx, s.nodeID)
			if err != nil {
				return
			}
			s.mu.Lock()
			if snapshot.Weekly != nil {
				remaining, state, ok := dailyRemainder(
					snapshot.Weekly.UsedPercent, snapshot.Weekly.ResetsAt,
					s.daily[collector.Backend()], time.Now(),
				)
				if ok {
					snapshot.TodayRemaining = &remaining
					s.daily[collector.Backend()] = state
				}
			}
			s.snapshots[collector.Backend()] = snapshot
			s.mu.Unlock()
		}()
	}
	workers.Wait()
	s.mu.Lock()
	for backend := range s.snapshots {
		if !connected[strings.ToLower(backend)] {
			delete(s.snapshots, backend)
			delete(s.daily, backend)
		}
	}
	s.mu.Unlock()
}

func (s *Store) connectedBackends() map[string]bool {
	result := make(map[string]bool)
	state := s.state.State()
	if state == nil {
		return result
	}
	node, ok := state.Nodes[s.nodeID]
	if !ok {
		return result
	}
	for _, backend := range node.Backends {
		result[strings.ToLower(strings.TrimSpace(backend.Name))] = true
	}
	return result
}

func quotaPollMinutes(state *domain.State) int {
	if state != nil {
		for userID, access := range state.Users {
			if access.Role == domain.RoleOwner {
				return state.Preferences[userID].EffectiveQuotaPollMinutes()
			}
		}
	}
	return 10
}
