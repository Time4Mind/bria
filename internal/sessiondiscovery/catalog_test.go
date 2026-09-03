package sessiondiscovery_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessiondiscovery"
)

func TestDiscoverAllMergesProviderSourcesIntoOneDeterministicCatalog(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	codex := sessiondiscovery.Record{
		Provider: domain.ProviderCodex, ProviderSessionID: "thread-1",
		ComputerID: "macbook", Workdir: "/work/project", HistoryRef: "codex://thread/thread-1",
		CreatedAt: now, UpdatedAt: now,
	}
	claude := sessiondiscovery.Record{
		Provider: domain.ProviderClaude, ProviderSessionID: "00000000-0000-4000-8000-000000000001",
		ComputerID: "linux", Workdir: "/srv/project", HistoryRef: "claude://session/00000000-0000-4000-8000-000000000001",
		CreatedAt: now, UpdatedAt: now.Add(time.Hour),
	}

	report, err := sessiondiscovery.DiscoverAll(context.Background(),
		staticSource{records: []sessiondiscovery.Record{codex}},
		staticSource{records: []sessiondiscovery.Record{claude}},
	)
	if err != nil {
		t.Fatalf("DiscoverAll() error = %v", err)
	}
	if want := []sessiondiscovery.Record{claude, codex}; !reflect.DeepEqual(report.Records, want) {
		t.Fatalf("records = %#v, want one unified catalog %#v", report.Records, want)
	}
}

func TestDiscoverAllReturnsUsableRecordsAndReportsIncompleteSourceCoverage(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	partial := sessiondiscovery.Record{
		Provider: domain.ProviderClaude, ProviderSessionID: "00000000-0000-4000-8000-000000000003",
		ComputerID: "partial", Workdir: "/srv/partial", HistoryRef: "claude://session/00000000-0000-4000-8000-000000000003",
		CreatedAt: now, UpdatedAt: now.Add(time.Hour),
	}
	available := sessiondiscovery.Record{
		Provider: domain.ProviderCodex, ProviderSessionID: "thread-available",
		ComputerID: "available", Workdir: "/work/project", HistoryRef: "codex://thread/thread-available",
		CreatedAt: now, UpdatedAt: now,
	}
	unavailable := errors.New("executor unavailable")

	report, err := sessiondiscovery.DiscoverAll(context.Background(),
		staticSource{records: []sessiondiscovery.Record{partial}, err: unavailable},
		staticSource{records: []sessiondiscovery.Record{available}},
	)

	if !errors.Is(err, unavailable) {
		t.Fatalf("DiscoverAll() error = %v, want source error", err)
	}
	if want := []sessiondiscovery.Record{partial, available}; !reflect.DeepEqual(report.Records, want) {
		t.Fatalf("records = %#v, want valid source prefix and available provider sessions %#v", report.Records, want)
	}
}

func TestDiscoverAllKeepsCanceledSourcePrefixAndDoesNotStartLaterSources(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	prefix := sessiondiscovery.Record{
		Provider: domain.ProviderCodex, ProviderSessionID: "thread-before-cancel",
		ComputerID: "macbook", Workdir: "/work/project", HistoryRef: "codex://thread/thread-before-cancel",
		CreatedAt: now, UpdatedAt: now,
	}
	laterCalls := 0

	report, err := sessiondiscovery.DiscoverAll(context.Background(),
		staticSource{records: []sessiondiscovery.Record{prefix}, err: context.Canceled},
		funcSource(func(context.Context) ([]sessiondiscovery.Record, error) {
			laterCalls++
			return nil, nil
		}),
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverAll() error = %v, want context cancellation", err)
	}
	if !reflect.DeepEqual(report.Records, []sessiondiscovery.Record{prefix}) {
		t.Fatalf("records = %#v, want completed prefix retained", report.Records)
	}
	if laterCalls != 0 {
		t.Fatalf("later source calls = %d, want 0 after cancellation", laterCalls)
	}
}

func TestMergeDeduplicatesSameProviderSessionWithoutOriginGroups(t *testing.T) {
	created := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	first := sessiondiscovery.Record{
		Provider: domain.ProviderCodex, ProviderSessionID: "thread-123",
		ComputerID: "macbook", Workdir: "/work/project",
		HistoryRef: "codex://thread/thread-123", CreatedAt: created,
		UpdatedAt: created.Add(time.Hour),
	}
	newerObservation := first
	newerObservation.UpdatedAt = created.Add(2 * time.Hour)

	report := sessiondiscovery.Merge(first, newerObservation)

	want := newerObservation
	if !reflect.DeepEqual(report.Records, []sessiondiscovery.Record{want}) {
		t.Fatalf("records = %#v, want one merged record %#v", report.Records, want)
	}
	if len(report.Rejections) != 0 {
		t.Fatalf("rejections = %#v, want none", report.Rejections)
	}
}

func TestMergeRejectsCorruptRecordsWithoutHidingValidSessions(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	valid := sessiondiscovery.Record{
		Provider: domain.ProviderClaude, ProviderSessionID: "00000000-0000-4000-8000-000000000001",
		ComputerID: "linux", Workdir: "/srv/work", HistoryRef: "claude://session/00000000-0000-4000-8000-000000000001",
		CreatedAt: now, UpdatedAt: now,
	}
	cases := []sessiondiscovery.Record{
		{},
		withRecord(valid, func(record *sessiondiscovery.Record) { record.Provider = "other" }),
		withRecord(valid, func(record *sessiondiscovery.Record) { record.ProviderSessionID = " session " }),
		withRecord(valid, func(record *sessiondiscovery.Record) { record.ComputerID = "" }),
		withRecord(valid, func(record *sessiondiscovery.Record) { record.Workdir = "relative/work" }),
		withRecord(valid, func(record *sessiondiscovery.Record) { record.HistoryRef = "" }),
		withRecord(valid, func(record *sessiondiscovery.Record) { record.CreatedAt = time.Time{} }),
		withRecord(valid, func(record *sessiondiscovery.Record) { record.UpdatedAt = now.Add(-time.Second) }),
	}

	report := sessiondiscovery.Merge(append(cases, valid)...)

	if !reflect.DeepEqual(report.Records, []sessiondiscovery.Record{valid}) {
		t.Fatalf("records = %#v, want only valid record", report.Records)
	}
	if len(report.Rejections) != len(cases) {
		t.Fatalf("rejections = %d, want %d: %#v", len(report.Rejections), len(cases), report.Rejections)
	}
	for index, rejection := range report.Rejections {
		if !errors.Is(rejection.Err, sessiondiscovery.ErrInvalidRecord) {
			t.Errorf("rejection %d error = %v, want ErrInvalidRecord", index, rejection.Err)
		}
	}
}

func TestMergeRejectsEveryConflictingObservationForAnAmbiguousIdentity(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	first := sessiondiscovery.Record{
		Provider: domain.ProviderCodex, ProviderSessionID: "same-thread",
		ComputerID: "macbook", Workdir: "/work/one", HistoryRef: "codex://thread/same-thread",
		CreatedAt: now, UpdatedAt: now.Add(time.Hour),
	}
	conflict := first
	conflict.Workdir = "/work/two"

	report := sessiondiscovery.Merge(first, conflict)

	if len(report.Records) != 0 {
		t.Fatalf("records = %#v, want ambiguous identity excluded", report.Records)
	}
	if len(report.Rejections) != 2 {
		t.Fatalf("rejections = %#v, want both conflicting observations", report.Rejections)
	}
	for index, rejection := range report.Rejections {
		if !errors.Is(rejection.Err, sessiondiscovery.ErrAmbiguousRecord) {
			t.Errorf("rejection %d error = %v, want ErrAmbiguousRecord", index, rejection.Err)
		}
	}
}

func TestMergeReportsAmbiguousIdentitiesInFirstObservationOrder(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	firstIdentity := sessiondiscovery.Record{
		Provider: domain.ProviderCodex, ProviderSessionID: "z-first-observed",
		ComputerID: "macbook", Workdir: "/work/one", HistoryRef: "codex://thread/z-first-observed",
		CreatedAt: now, UpdatedAt: now,
	}
	secondIdentity := sessiondiscovery.Record{
		Provider: domain.ProviderClaude, ProviderSessionID: "00000000-0000-4000-8000-000000000002",
		ComputerID: "linux", Workdir: "/srv/one", HistoryRef: "claude://session/00000000-0000-4000-8000-000000000002",
		CreatedAt: now, UpdatedAt: now,
	}
	firstConflict := firstIdentity
	firstConflict.Workdir = "/work/two"
	secondConflict := secondIdentity
	secondConflict.HistoryRef = "claude://session/conflict"

	for attempt := 0; attempt < 32; attempt++ {
		report := sessiondiscovery.Merge(firstIdentity, secondIdentity, firstConflict, secondConflict)
		got := make([]string, 0, len(report.Rejections))
		for _, rejection := range report.Rejections {
			got = append(got, rejection.Record.ProviderSessionID)
		}
		want := []string{"z-first-observed", "z-first-observed", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000002"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("attempt %d rejection identities = %#v, want stable first-observation order %#v", attempt, got, want)
		}
	}
}

func withRecord(record sessiondiscovery.Record, change func(*sessiondiscovery.Record)) sessiondiscovery.Record {
	change(&record)
	return record
}

type staticSource struct {
	records []sessiondiscovery.Record
	err     error
}

type funcSource func(context.Context) ([]sessiondiscovery.Record, error)

func (source funcSource) Discover(ctx context.Context) ([]sessiondiscovery.Record, error) {
	return source(ctx)
}

func (source staticSource) Discover(context.Context) ([]sessiondiscovery.Record, error) {
	return append([]sessiondiscovery.Record(nil), source.records...), source.err
}
