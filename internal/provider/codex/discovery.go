package codex

import (
	"context"
	"errors"
	"fmt"

	"bria/internal/domain"
	"bria/internal/sessiondiscovery"
)

const (
	defaultDiscoveryPageSize   = uint32(100)
	defaultDiscoveryMaxPages   = 100
	defaultDiscoveryMaxRecords = 10_000
)

var (
	ErrInvalidDiscoveryConfig = errors.New("invalid codex discovery configuration")
	ErrDiscoveryLimit         = errors.New("codex discovery limit reached")
)

type DiscoveryOptions struct {
	PageSize   uint32
	MaxPages   int
	MaxRecords int
}

// DiscoverySource projects official app-server thread/list metadata into the
// provider-neutral discovery catalog. Pages are committed atomically: if one
// item is corrupt, only records from earlier complete pages are returned.
type DiscoverySource struct {
	lister     ThreadLister
	computerID domain.ComputerID
	pageSize   uint32
	maxPages   int
	maxRecords int
}

type discoveryPageError struct {
	page  int
	cause error
}

func (failure *discoveryPageError) Error() string {
	return fmt.Sprintf("codex thread/list page %d failed", failure.page)
}

func (failure *discoveryPageError) Unwrap() error {
	return failure.cause
}

func NewDiscoverySource(lister ThreadLister, computerID domain.ComputerID, options DiscoveryOptions) (*DiscoverySource, error) {
	if lister == nil || !boundedExactText(string(computerID), 256, false) {
		return nil, ErrInvalidDiscoveryConfig
	}
	if options.PageSize == 0 {
		options.PageSize = defaultDiscoveryPageSize
	}
	if options.MaxPages == 0 {
		options.MaxPages = defaultDiscoveryMaxPages
	}
	if options.MaxRecords == 0 {
		options.MaxRecords = defaultDiscoveryMaxRecords
	}
	if options.MaxPages < 1 || options.MaxRecords < 1 || uint64(options.PageSize) > uint64(options.MaxRecords) {
		return nil, ErrInvalidDiscoveryConfig
	}
	return &DiscoverySource{
		lister: lister, computerID: computerID, pageSize: options.PageSize,
		maxPages: options.MaxPages, maxRecords: options.MaxRecords,
	}, nil
}

func (source *DiscoverySource) Discover(ctx context.Context) ([]sessiondiscovery.Record, error) {
	if source == nil || source.lister == nil {
		return nil, ErrInvalidDiscoveryConfig
	}
	records := make([]sessiondiscovery.Record, 0, min(source.maxRecords, int(source.pageSize)))
	cursor := ""
	seenCursors := make(map[string]struct{}, source.maxPages)
	for pageNumber := 1; pageNumber <= source.maxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return records, err
		}
		page, err := source.lister.ListThreads(ctx, ThreadListRequest{Cursor: cursor, Limit: source.pageSize})
		if err != nil {
			return records, &discoveryPageError{page: pageNumber, cause: err}
		}
		if len(page.Threads) > int(source.pageSize) || len(records)+len(page.Threads) > source.maxRecords {
			return records, ErrDiscoveryLimit
		}
		pageRecords := make([]sessiondiscovery.Record, 0, len(page.Threads))
		for _, thread := range page.Threads {
			record := sessiondiscovery.Record{
				Provider: domain.ProviderCodex, ProviderSessionID: thread.ID,
				ComputerID: source.computerID, Workdir: thread.Cwd,
				HistoryRef: "codex://thread/" + thread.ID,
				CreatedAt:  thread.CreatedAt, UpdatedAt: thread.UpdatedAt,
			}
			validated := sessiondiscovery.Merge(record)
			if len(validated.Rejections) != 0 {
				return records, &discoveryPageError{page: pageNumber, cause: validated.Rejections[0].Err}
			}
			pageRecords = append(pageRecords, validated.Records[0])
		}
		records = append(records, pageRecords...)
		if page.NextCursor == "" {
			return records, nil
		}
		if !boundedExactText(page.NextCursor, 16*1024, false) {
			return records, ErrInvalidResponse
		}
		if _, duplicate := seenCursors[page.NextCursor]; duplicate || page.NextCursor == cursor {
			return records, ErrInvalidResponse
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
		if len(records) == source.maxRecords {
			return records, ErrDiscoveryLimit
		}
	}
	return records, ErrDiscoveryLimit
}

var _ sessiondiscovery.Source = (*DiscoverySource)(nil)
