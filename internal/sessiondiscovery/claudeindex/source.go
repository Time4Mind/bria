// Package claudeindex adapts Claude's read-only transcript summary port to the
// provider-neutral session discovery contract.
package claudeindex

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"bria/internal/claudestore"
	"bria/internal/domain"
	"bria/internal/sessiondiscovery"
)

var ErrInvalidConfiguration = errors.New("invalid claude discovery adapter configuration")

type Lister interface {
	List(context.Context) ([]claudestore.SessionSummary, error)
}

type Source struct {
	lister     Lister
	computerID domain.ComputerID
}

func New(lister Lister, computerID domain.ComputerID) (*Source, error) {
	if lister == nil || computerID == "" || strings.TrimSpace(string(computerID)) != string(computerID) {
		return nil, ErrInvalidConfiguration
	}
	return &Source{lister: lister, computerID: computerID}, nil
}

func (source *Source) Discover(ctx context.Context) ([]sessiondiscovery.Record, error) {
	if source == nil || source.lister == nil || ctx == nil {
		return nil, ErrInvalidConfiguration
	}
	summaries, listErr := source.lister.List(ctx)
	records := make([]sessiondiscovery.Record, 0, len(summaries))
	var validationErrors []error
	for _, summary := range summaries {
		if !canonicalUUID(summary.ID) {
			validationErrors = append(validationErrors, sessiondiscovery.ErrInvalidRecord)
			continue
		}
		record := sessiondiscovery.Record{
			Provider: domain.ProviderClaude, ProviderSessionID: summary.ID,
			ComputerID: source.computerID, Workdir: summary.Cwd,
			HistoryRef: "claude://session/" + summary.ID,
			CreatedAt:  summary.CreatedAt, UpdatedAt: summary.UpdatedAt,
		}
		validated := sessiondiscovery.Merge(record)
		if len(validated.Rejections) != 0 {
			validationErrors = append(validationErrors, validated.Rejections[0].Err)
			continue
		}
		records = append(records, validated.Records[0])
	}
	merged := sessiondiscovery.Merge(records...)
	for _, rejection := range merged.Rejections {
		validationErrors = append(validationErrors, rejection.Err)
	}
	return merged.Records, errors.Join(append(validationErrors, listErr)...)
}

func canonicalUUID(value string) bool {
	if len(value) != 36 || value != strings.ToLower(value) || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16
}

var _ sessiondiscovery.Source = (*Source)(nil)
