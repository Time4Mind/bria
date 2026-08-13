package archive

import (
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type RetentionDays int

const (
	RetentionUnlimited RetentionDays = 0
	Retention14Days    RetentionDays = 14
	Retention30Days    RetentionDays = 30
)

type IdleHours int

const (
	IdleUnlimited IdleHours = 0
	Idle6Hours    IdleHours = 6
	Idle12Hours   IdleHours = 12
	Idle24Hours   IdleHours = 24
)

const (
	ExpiryRecordOnly = domain.ArchiveRemoveRecord
	ExpiryFull       = domain.ArchiveRemoveAll
)

type RetentionPolicy struct {
	Days   RetentionDays              `json:"days"`
	Action domain.ArchiveExpiryAction `json:"action"`
}

func (p RetentionPolicy) Validate() error {
	if p.Days != RetentionUnlimited && p.Days != Retention14Days &&
		p.Days != Retention30Days {
		return fmt.Errorf("archive retention must be 0, 14, or 30 days")
	}
	if p.Action != ExpiryRecordOnly && p.Action != ExpiryFull {
		return fmt.Errorf("unsupported archive expiry action: %q", p.Action)
	}
	return nil
}

func (p RetentionPolicy) DueAt(archivedAt time.Time) (time.Time, bool, error) {
	if err := p.Validate(); err != nil {
		return time.Time{}, false, err
	}
	if archivedAt.IsZero() {
		return time.Time{}, false, fmt.Errorf("archived timestamp is required")
	}
	if p.Days == RetentionUnlimited {
		return time.Time{}, false, nil
	}
	due := archivedAt.UTC().Add(time.Duration(p.Days) * 24 * time.Hour)
	return due, true, nil
}

type IdlePolicy struct {
	Hours IdleHours `json:"hours"`
}

func (p IdlePolicy) Validate() error {
	if p.Hours != IdleUnlimited && p.Hours != Idle6Hours &&
		p.Hours != Idle12Hours && p.Hours != Idle24Hours {
		return fmt.Errorf("idle archive must be 0, 6, 12, or 24 hours")
	}
	return nil
}

func (p IdlePolicy) DueAt(lastActivityAt time.Time) (time.Time, bool, error) {
	if err := p.Validate(); err != nil {
		return time.Time{}, false, err
	}
	if lastActivityAt.IsZero() {
		return time.Time{}, false, fmt.Errorf("last activity timestamp is required")
	}
	if p.Hours == IdleUnlimited {
		return time.Time{}, false, nil
	}
	due := lastActivityAt.UTC().Add(time.Duration(p.Hours) * time.Hour)
	return due, true, nil
}

type Policy struct {
	Retention RetentionPolicy `json:"retention"`
	Idle      IdlePolicy      `json:"idle"`
}

func (p Policy) Validate() error {
	if err := p.Retention.Validate(); err != nil {
		return err
	}
	return p.Idle.Validate()
}

func PolicyFromPreferences(preferences domain.UserPreferences) Policy {
	return Policy{
		Retention: RetentionPolicy{
			Days:   RetentionDays(preferences.ArchiveRetentionDays),
			Action: preferences.ArchiveExpiryAction,
		},
		Idle: IdlePolicy{Hours: IdleHours(preferences.IdleArchiveHours)},
	}
}
