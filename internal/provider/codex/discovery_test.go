package codex_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/provider/codex"
	"bria/internal/sessiondiscovery"
)

func TestDiscoverySourcePaginatesWithStableProviderBinding(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	lister := &recordingThreadLister{pages: []codex.ThreadListPage{
		{
			Threads:    []codex.ThreadSummary{{ID: "thread-2", Cwd: "/work/two", CreatedAt: created, UpdatedAt: created.Add(2 * time.Hour)}},
			NextCursor: "opaque-cursor",
		},
		{
			Threads: []codex.ThreadSummary{{ID: "thread-1", Cwd: "/work/one", CreatedAt: created, UpdatedAt: created.Add(time.Hour)}},
		},
	}}
	source, err := codex.NewDiscoverySource(lister, domain.ComputerID("macbook"), codex.DiscoveryOptions{
		PageSize: 2, MaxPages: 3, MaxRecords: 4,
	})
	if err != nil {
		t.Fatalf("NewDiscoverySource() error = %v", err)
	}

	records, err := source.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	wantRecords := []sessiondiscovery.Record{
		{Provider: domain.ProviderCodex, ProviderSessionID: "thread-2", ComputerID: "macbook", Workdir: "/work/two", HistoryRef: "codex://thread/thread-2", CreatedAt: created, UpdatedAt: created.Add(2 * time.Hour)},
		{Provider: domain.ProviderCodex, ProviderSessionID: "thread-1", ComputerID: "macbook", Workdir: "/work/one", HistoryRef: "codex://thread/thread-1", CreatedAt: created, UpdatedAt: created.Add(time.Hour)},
	}
	if !reflect.DeepEqual(records, wantRecords) {
		t.Fatalf("Discover() = %#v, want %#v", records, wantRecords)
	}
	wantRequests := []codex.ThreadListRequest{{Limit: 2}, {Cursor: "opaque-cursor", Limit: 2}}
	if !reflect.DeepEqual(lister.requests, wantRequests) {
		t.Fatalf("ListThreads requests = %#v, want %#v", lister.requests, wantRequests)
	}
	var _ sessiondiscovery.Source = source
}

func TestDiscoverySourceReturnsOnlyCompletedPagesOnLaterCorruption(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	lister := &recordingThreadLister{pages: []codex.ThreadListPage{
		{
			Threads:    []codex.ThreadSummary{{ID: "thread-good", Cwd: "/work", CreatedAt: now, UpdatedAt: now}},
			NextCursor: "cursor-2",
		},
		{
			Threads: []codex.ThreadSummary{{ID: "thread-bad", Cwd: "relative", CreatedAt: now, UpdatedAt: now}},
		},
	}}
	source, err := codex.NewDiscoverySource(lister, "macbook", codex.DiscoveryOptions{PageSize: 1, MaxPages: 3, MaxRecords: 3})
	if err != nil {
		t.Fatalf("NewDiscoverySource() error = %v", err)
	}

	records, err := source.Discover(context.Background())
	if !errors.Is(err, sessiondiscovery.ErrInvalidRecord) {
		t.Fatalf("Discover() error = %v, want ErrInvalidRecord", err)
	}
	want := []sessiondiscovery.Record{{
		Provider: domain.ProviderCodex, ProviderSessionID: "thread-good", ComputerID: "macbook",
		Workdir: "/work", HistoryRef: "codex://thread/thread-good", CreatedAt: now, UpdatedAt: now,
	}}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("Discover() partial records = %#v, want completed first page %#v", records, want)
	}
}

func TestDiscoverySourceReturnsCompletedPagesAndRedactsLaterTransportError(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	transportErr := errors.New("transport failed with secret-value")
	lister := &recordingThreadLister{
		pages: []codex.ThreadListPage{{
			Threads:    []codex.ThreadSummary{{ID: "thread-good", Cwd: "/work", CreatedAt: now, UpdatedAt: now}},
			NextCursor: "cursor-2",
		}},
		terminalErr: transportErr,
	}
	source, err := codex.NewDiscoverySource(lister, "macbook", codex.DiscoveryOptions{PageSize: 1, MaxPages: 3, MaxRecords: 3})
	if err != nil {
		t.Fatalf("NewDiscoverySource() error = %v", err)
	}

	records, err := source.Discover(context.Background())
	if !errors.Is(err, transportErr) {
		t.Fatalf("Discover() error = %v, want wrapped transport identity", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("Discover() error leaked provider text: %q", err)
	}
	if len(records) != 1 || records[0].ProviderSessionID != "thread-good" {
		t.Fatalf("Discover() partial records = %#v, want completed first page", records)
	}
}

func TestDiscoverySourceStopsAtBoundWithoutFollowingUnboundedCursor(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	lister := &recordingThreadLister{pages: []codex.ThreadListPage{{
		Threads:    []codex.ThreadSummary{{ID: "thread-1", Cwd: "/work", CreatedAt: now, UpdatedAt: now}},
		NextCursor: "more",
	}}}
	source, err := codex.NewDiscoverySource(lister, "macbook", codex.DiscoveryOptions{PageSize: 1, MaxPages: 1, MaxRecords: 1})
	if err != nil {
		t.Fatalf("NewDiscoverySource() error = %v", err)
	}

	records, err := source.Discover(context.Background())
	if !errors.Is(err, codex.ErrDiscoveryLimit) {
		t.Fatalf("Discover() error = %v, want ErrDiscoveryLimit", err)
	}
	if len(records) != 1 || len(lister.requests) != 1 {
		t.Fatalf("Discover() returned %d records after %d requests, want bounded 1/1", len(records), len(lister.requests))
	}
}

type recordingThreadLister struct {
	requests    []codex.ThreadListRequest
	pages       []codex.ThreadListPage
	terminalErr error
}

func (lister *recordingThreadLister) ListThreads(_ context.Context, request codex.ThreadListRequest) (codex.ThreadListPage, error) {
	lister.requests = append(lister.requests, request)
	if len(lister.pages) == 0 {
		if lister.terminalErr != nil {
			return codex.ThreadListPage{}, lister.terminalErr
		}
		return codex.ThreadListPage{}, errors.New("unexpected request")
	}
	page := lister.pages[0]
	lister.pages = lister.pages[1:]
	return page, nil
}
