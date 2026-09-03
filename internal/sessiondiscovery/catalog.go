// Package sessiondiscovery validates and merges provider-native sessions that
// were created outside Bria. It stores references and exact provider IDs, not
// copied provider history.
package sessiondiscovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"bria/internal/domain"
)

var (
	// ErrInvalidRecord marks corrupt or incomplete provider metadata.
	ErrInvalidRecord = errors.New("invalid discovered session record")
	// ErrAmbiguousRecord marks conflicting metadata for one provider identity.
	ErrAmbiguousRecord = errors.New("ambiguous discovered session record")
	// ErrInvalidSource marks a missing explicitly configured discovery source.
	ErrInvalidSource = errors.New("invalid session discovery source")
)

// Record is the provider-neutral metadata needed to show and exactly resume a
// provider-native session. HistoryRef is an opaque reference to provider-owned
// history; it is deliberately not history contents.
type Record struct {
	Provider          domain.Provider
	ProviderSessionID string
	ComputerID        domain.ComputerID
	Workdir           string
	HistoryRef        string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Rejection describes a record excluded from the catalog.
type Rejection struct {
	Record Record
	Err    error
}

// Report is one deterministic discovery result.
type Report struct {
	Records    []Record
	Rejections []Rejection
}

// Source discovers provider-native session metadata without copying history.
type Source interface {
	Discover(context.Context) ([]Record, error)
}

// DiscoverAll reads explicitly configured provider sources and returns one
// provider-neutral catalog. Sources decide where metadata lives; this package
// never scans a home directory or copies provider history.
func DiscoverAll(ctx context.Context, sources ...Source) (Report, error) {
	records := make([]Record, 0)
	var sourceErrors []error
	for index, source := range sources {
		if err := ctx.Err(); err != nil {
			return Merge(records...), errors.Join(append(sourceErrors, err)...)
		}
		if source == nil {
			sourceErrors = append(sourceErrors, fmt.Errorf("source %d: %w", index, ErrInvalidSource))
			continue
		}
		discovered, err := source.Discover(ctx)
		records = append(records, discovered...)
		if err != nil {
			sourceErrors = append(sourceErrors, fmt.Errorf("source %d: %w", index, err))
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return Merge(records...), errors.Join(sourceErrors...)
			}
			continue
		}
	}
	return Merge(records...), errors.Join(sourceErrors...)
}

// Merge validates and deduplicates records into one origin-neutral catalog.
func Merge(records ...Record) Report {
	byIdentity := make(map[identity][]Record, len(records))
	identityOrder := make([]identity, 0, len(records))
	rejections := make([]Rejection, 0)
	for _, record := range records {
		validated, err := validateRecord(record)
		if err != nil {
			rejections = append(rejections, Rejection{Record: record, Err: err})
			continue
		}
		record = validated
		key := recordIdentity(record)
		if _, exists := byIdentity[key]; !exists {
			identityOrder = append(identityOrder, key)
		}
		byIdentity[key] = append(byIdentity[key], record)
	}
	merged := make([]Record, 0, len(byIdentity))
	for _, key := range identityOrder {
		observations := byIdentity[key]
		current := observations[0]
		ambiguous := false
		for _, observation := range observations[1:] {
			if !sameBinding(current, observation) {
				ambiguous = true
			}
			if observation.UpdatedAt.After(current.UpdatedAt) {
				current = observation
			}
		}
		if ambiguous {
			for _, observation := range observations {
				rejections = append(rejections, Rejection{
					Record: observation,
					Err:    fmt.Errorf("%w: conflicting metadata for provider identity", ErrAmbiguousRecord),
				})
			}
			continue
		}
		merged = append(merged, current)
	}
	sort.Slice(merged, func(left, right int) bool {
		if !merged[left].UpdatedAt.Equal(merged[right].UpdatedAt) {
			return merged[left].UpdatedAt.After(merged[right].UpdatedAt)
		}
		return recordIdentity(merged[left]).less(recordIdentity(merged[right]))
	})
	return Report{Records: merged, Rejections: rejections}
}

func sameBinding(left, right Record) bool {
	return left.Provider == right.Provider &&
		left.ProviderSessionID == right.ProviderSessionID &&
		left.ComputerID == right.ComputerID &&
		left.Workdir == right.Workdir &&
		left.HistoryRef == right.HistoryRef &&
		left.CreatedAt.Equal(right.CreatedAt)
}

func validateRecord(record Record) (Record, error) {
	if record.Provider != domain.ProviderCodex && record.Provider != domain.ProviderClaude {
		return Record{}, invalid("unsupported provider %q", record.Provider)
	}
	if !saneExactText(record.ProviderSessionID, 1024) {
		return Record{}, invalid("provider session id is missing or malformed")
	}
	if !saneExactText(string(record.ComputerID), 256) {
		return Record{}, invalid("computer id is missing or malformed")
	}
	if !saneExactText(record.Workdir, 16*1024) || !portableAbsolute(record.Workdir) {
		return Record{}, invalid("workdir is not an absolute provider path")
	}
	if !saneExactText(record.HistoryRef, 16*1024) {
		return Record{}, invalid("history reference is missing or malformed")
	}
	if record.CreatedAt.IsZero() {
		return Record{}, invalid("created timestamp is required")
	}
	if record.UpdatedAt.IsZero() {
		return Record{}, invalid("updated timestamp is required")
	}
	if record.UpdatedAt.Before(record.CreatedAt) {
		return Record{}, invalid("updated timestamp precedes creation")
	}
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRecord, fmt.Sprintf(format, args...))
}

func saneExactText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

// portableAbsolute recognizes provider workdirs transported between unlike
// coordinator and executor operating systems. It does not reinterpret or
// normalize the provider's exact path.
func portableAbsolute(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && isASCIILetter(value[0]) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

type identity struct {
	computer domain.ComputerID
	provider domain.Provider
	session  string
}

func recordIdentity(record Record) identity {
	return identity{computer: record.ComputerID, provider: record.Provider, session: record.ProviderSessionID}
}

func (left identity) less(right identity) bool {
	if left.computer != right.computer {
		return left.computer < right.computer
	}
	if left.provider != right.provider {
		return left.provider < right.provider
	}
	return left.session < right.session
}
