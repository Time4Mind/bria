package claudeindex_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bria/internal/claudestore"
	"bria/internal/domain"
	"bria/internal/sessiondiscovery"
	"bria/internal/sessiondiscovery/claudeindex"
)

func TestSourceProjectsExactClaudeStoreSummary(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	summary := claudestore.SessionSummary{
		ID: "00000000-0000-4000-8000-000000000041", Cwd: "/work/project",
		CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}
	source, err := claudeindex.New(staticLister{summaries: []claudestore.SessionSummary{summary}}, domain.ComputerID("macbook"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	records, err := source.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	want := []sessiondiscovery.Record{{
		Provider: domain.ProviderClaude, ProviderSessionID: summary.ID,
		ComputerID: "macbook", Workdir: summary.Cwd,
		HistoryRef: "claude://session/" + summary.ID,
		CreatedAt:  summary.CreatedAt, UpdatedAt: summary.UpdatedAt,
	}}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("Discover() = %#v, want %#v", records, want)
	}
	var _ sessiondiscovery.Source = source
}

func TestSourcePreservesValidPrefixAndStoreFailure(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	summary := claudestore.SessionSummary{
		ID: "00000000-0000-4000-8000-000000000042", Cwd: "/work/project",
		CreatedAt: now, UpdatedAt: now,
	}
	storeFailure := errors.New("sanitized store failure")
	source, err := claudeindex.New(staticLister{summaries: []claudestore.SessionSummary{summary}, err: storeFailure}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	records, err := source.Discover(context.Background())
	if !errors.Is(err, storeFailure) || len(records) != 1 || records[0].ProviderSessionID != summary.ID {
		t.Fatalf("Discover() = (%#v, %v), want valid prefix plus store error", records, err)
	}
}

type staticLister struct {
	summaries []claudestore.SessionSummary
	err       error
}

func (lister staticLister) List(context.Context) ([]claudestore.SessionSummary, error) {
	return append([]claudestore.SessionSummary(nil), lister.summaries...), lister.err
}
