package archive_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestRetentionDueAtChoices(t *testing.T) {
	archivedAt := time.Date(2026, 8, 10, 3, 4, 5, 0, time.FixedZone("test", 3*60*60))
	tests := []struct {
		name   string
		policy archive.RetentionPolicy
		finite bool
		want   time.Time
	}{
		{
			name: "fourteen days",
			policy: archive.RetentionPolicy{
				Days: archive.Retention14Days, Action: archive.ExpiryRecordOnly,
			},
			finite: true,
			want:   archivedAt.UTC().Add(14 * 24 * time.Hour),
		},
		{
			name: "thirty days",
			policy: archive.RetentionPolicy{
				Days: archive.Retention30Days, Action: archive.ExpiryFull,
			},
			finite: true,
			want:   archivedAt.UTC().Add(30 * 24 * time.Hour),
		},
		{
			name: "unlimited",
			policy: archive.RetentionPolicy{
				Days: archive.RetentionUnlimited, Action: archive.ExpiryRecordOnly,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, finite, err := test.policy.DueAt(archivedAt)
			if err != nil {
				t.Fatalf("DueAt: %v", err)
			}
			if finite != test.finite || !got.Equal(test.want) {
				t.Fatalf("DueAt = (%v, %v), want (%v, %v)", got, finite, test.want, test.finite)
			}
		})
	}
}

func TestIdleDueAtChoices(t *testing.T) {
	lastActivity := time.Unix(100, 0)
	for _, test := range []struct {
		hours  archive.IdleHours
		finite bool
	}{
		{archive.IdleUnlimited, false},
		{archive.Idle6Hours, true},
		{archive.Idle12Hours, true},
		{archive.Idle24Hours, true},
	} {
		policy := archive.IdlePolicy{Hours: test.hours}
		due, finite, err := policy.DueAt(lastActivity)
		if err != nil {
			t.Fatalf("DueAt(%d): %v", test.hours, err)
		}
		if finite != test.finite {
			t.Fatalf("finite(%d) = %v", test.hours, finite)
		}
		if finite && !due.Equal(lastActivity.Add(time.Duration(test.hours)*time.Hour)) {
			t.Fatalf("due(%d) = %v", test.hours, due)
		}
	}
}

func TestPolicyRejectsValuesOutsideClosedChoices(t *testing.T) {
	invalidRetention := archive.RetentionPolicy{Days: 7, Action: archive.ExpiryFull}
	if err := invalidRetention.Validate(); err == nil {
		t.Fatal("seven-day retention unexpectedly accepted")
	}
	invalidIdle := archive.IdlePolicy{Hours: 48}
	if err := invalidIdle.Validate(); err == nil {
		t.Fatal("48-hour idle policy unexpectedly accepted")
	}
}

func TestPolicyFromDomainPreferences(t *testing.T) {
	preferences := domain.UserPreferences{
		SessionView:          domain.ViewHostFirst,
		IdleArchiveHours:     12,
		ArchiveRetentionDays: 30,
		ArchiveExpiryAction:  domain.ArchiveRemoveAll,
	}
	policy := archive.PolicyFromPreferences(preferences)
	if err := policy.Validate(); err != nil {
		t.Fatalf("converted policy: %v", err)
	}
	if policy.Idle.Hours != archive.Idle12Hours ||
		policy.Retention.Days != archive.Retention30Days ||
		policy.Retention.Action != archive.ExpiryFull {
		t.Fatalf("converted policy = %#v", policy)
	}
}
