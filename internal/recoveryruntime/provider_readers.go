package recoveryruntime

import (
	"context"
	"reflect"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

// ProviderReaders routes reconciliation by immutable session provider.
type ProviderReaders struct {
	codex  sessionruntime.AcceptedTurnReader
	claude sessionruntime.AcceptedTurnReader
}

func NewProviderReaders(codex, claude sessionruntime.AcceptedTurnReader) (*ProviderReaders, error) {
	if nilReader(codex) || nilReader(claude) {
		return nil, ErrUnavailable
	}
	return &ProviderReaders{codex: codex, claude: claude}, nil
}
func nilReader(reader sessionruntime.AcceptedTurnReader) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	kind := value.Kind()
	return (kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface ||
		kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice) && value.IsNil()
}

func (readers *ProviderReaders) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	if readers == nil || ctx == nil || request.SessionID == "" || request.Binding.Provider != request.Provider {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	var reader sessionruntime.AcceptedTurnReader
	switch request.Provider {
	case domain.ProviderCodex:
		reader = readers.codex
	case domain.ProviderClaude:
		reader = readers.claude
	default:
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	result, err := reader.ReadAcceptedTurns(ctx, request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return sessionruntime.AcceptedTurnReconciliation{}, contextErr
		}
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	result.Turns = append([]sessionruntime.ReconciledAcceptedTurn(nil), result.Turns...)
	return result, nil
}

var _ sessionruntime.AcceptedTurnReader = (*ProviderReaders)(nil)
