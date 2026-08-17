package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type QuotaWindow struct {
	UsedPercent int       `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at,omitempty"`
}

// QuotaDailyBudget preserves the start-of-day baseline used to divide a
// weekly quota over the remaining local calendar days. It travels with the
// quota snapshot so a node restart cannot silently reset today's allowance.
type QuotaDailyBudget struct {
	Date         string    `json:"date"`
	ResetsAt     time.Time `json:"resets_at"`
	DayStartUsed int       `json:"day_start_used"`
	Budget       float64   `json:"budget"`
}

type QuotaSnapshot struct {
	NodeID         NodeID            `json:"node_id"`
	Backend        string            `json:"backend"`
	AccountID      string            `json:"account_id,omitempty"`
	AccountLabel   string            `json:"account_label,omitempty"`
	FiveHour       *QuotaWindow      `json:"five_hour,omitempty"`
	Weekly         *QuotaWindow      `json:"weekly,omitempty"`
	TodayRemaining *float64          `json:"today_remaining,omitempty"`
	DailyBudget    *QuotaDailyBudget `json:"daily_budget,omitempty"`
	CollectedAt    time.Time         `json:"collected_at"`
}

type TemporaryLeader struct {
	NodeID NodeID    `json:"node_id"`
	Until  time.Time `json:"until"`
}

func (q QuotaSnapshot) Validate() error {
	if err := (SessionRef{NodeID: q.NodeID, SessionID: "quota"}).Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(q.Backend) == "" || len(q.Backend) > 32 ||
		strings.ContainsAny(q.Backend, "\r\n\t") || q.CollectedAt.IsZero() {
		return fmt.Errorf("%w: invalid quota identity", ErrInvalidState)
	}
	if len(q.AccountID) > 256 || len(q.AccountLabel) > 256 ||
		strings.ContainsAny(q.AccountID+q.AccountLabel, "\r\n\t") {
		return fmt.Errorf("%w: quota account identity is too long", ErrInvalidState)
	}
	for _, window := range []*QuotaWindow{q.FiveHour, q.Weekly} {
		if window != nil && (window.UsedPercent < 0 || window.UsedPercent > 100) {
			return fmt.Errorf("%w: quota percent must be between 0 and 100", ErrInvalidState)
		}
	}
	if q.TodayRemaining != nil && (math.IsNaN(*q.TodayRemaining) ||
		math.IsInf(*q.TodayRemaining, 0) || *q.TodayRemaining < -100 || *q.TodayRemaining > 100) {
		return fmt.Errorf("%w: remaining daily quota must be between -100 and 100", ErrInvalidState)
	}
	if q.DailyBudget != nil {
		budget := q.DailyBudget
		_, dateErr := time.Parse("2006-01-02", budget.Date)
		if dateErr != nil || budget.ResetsAt.IsZero() || budget.DayStartUsed < 0 ||
			budget.DayStartUsed > 100 || math.IsNaN(budget.Budget) ||
			math.IsInf(budget.Budget, 0) || budget.Budget < 0 || budget.Budget > 100 {
			return fmt.Errorf("%w: invalid daily quota budget", ErrInvalidState)
		}
	}
	return nil
}

func (q QuotaSnapshot) Key() string {
	return string(q.NodeID) + "/" + strings.ToLower(strings.TrimSpace(q.Backend))
}

func ProviderAccountAliasKey(nodeID NodeID, backend string) string {
	return string(nodeID) + "/" + strings.ToLower(strings.TrimSpace(backend))
}

func (s *State) SetProviderAccountAlias(nodeID NodeID, backend, alias string) error {
	node, ok := s.Nodes[nodeID]
	backend = strings.TrimSpace(backend)
	alias = strings.TrimSpace(alias)
	if !ok || backend == "" || len(backend) > 32 || strings.ContainsAny(backend, "\r\n\t") {
		return ErrNotFound
	}
	found := false
	for _, descriptor := range node.Backends {
		if strings.EqualFold(descriptor.Name, backend) {
			backend, found = descriptor.Name, true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	if len(alias) > 64 || strings.ContainsAny(alias, "\r\n\t") {
		return fmt.Errorf("%w: provider account alias is invalid", ErrInvalidState)
	}
	if s.ProviderAccountAliases == nil {
		s.ProviderAccountAliases = make(map[string]string)
	}
	key := ProviderAccountAliasKey(nodeID, backend)
	if alias == "" {
		delete(s.ProviderAccountAliases, key)
	} else {
		s.ProviderAccountAliases[key] = alias
	}
	return nil
}

func (s *State) ProviderAccountAlias(nodeID NodeID, backend string) string {
	return s.ProviderAccountAliases[ProviderAccountAliasKey(nodeID, backend)]
}

func cloneQuota(snapshot QuotaSnapshot) QuotaSnapshot {
	if snapshot.FiveHour != nil {
		window := *snapshot.FiveHour
		snapshot.FiveHour = &window
	}
	if snapshot.Weekly != nil {
		window := *snapshot.Weekly
		snapshot.Weekly = &window
	}
	if snapshot.TodayRemaining != nil {
		remaining := *snapshot.TodayRemaining
		snapshot.TodayRemaining = &remaining
	}
	if snapshot.DailyBudget != nil {
		budget := *snapshot.DailyBudget
		snapshot.DailyBudget = &budget
	}
	return snapshot
}

func (s *State) PublishNodeQuotas(nodeID NodeID, snapshots []QuotaSnapshot) error {
	if _, ok := s.Nodes[nodeID]; !ok {
		return ErrNotFound
	}
	if len(snapshots) > 16 {
		return fmt.Errorf("%w: too many quota snapshots", ErrInvalidState)
	}
	if s.Quotas == nil {
		s.Quotas = make(map[string]QuotaSnapshot)
	}
	for _, snapshot := range snapshots {
		if snapshot.NodeID != nodeID {
			return fmt.Errorf("%w: quota node mismatch", ErrInvalidState)
		}
		if err := snapshot.Validate(); err != nil {
			return err
		}
		if current, ok := s.Quotas[snapshot.Key()]; ok &&
			snapshot.CollectedAt.Before(current.CollectedAt) {
			continue
		}
		s.Quotas[snapshot.Key()] = cloneQuota(snapshot)
	}
	return nil
}

func (s *State) RequestQuotaRefresh(at time.Time) {
	if at.After(s.QuotaRefreshRequestedAt) {
		s.QuotaRefreshRequestedAt = at
	}
}

func (s *State) SetTemporaryLeader(nodeID NodeID, until time.Time, at time.Time) error {
	node, ok := s.Nodes[nodeID]
	if !ok || node.Status == NodeOffline || !until.After(at) || until.After(at.Add(time.Hour)) {
		return ErrInvalidState
	}
	s.TemporaryLeader = TemporaryLeader{NodeID: nodeID, Until: until}
	return nil
}

func (s *State) ClearTemporaryLeader(nodeID NodeID, observedUntil time.Time) {
	if s.TemporaryLeader.NodeID == nodeID && s.TemporaryLeader.Until.Equal(observedUntil) {
		s.TemporaryLeader = TemporaryLeader{}
	}
}
