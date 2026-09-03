package recoveryruntime

import (
	"context"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"bria/internal/claudestore"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

// ClaudeReader bridges Claude's provider-native transcript identities to the
// provider-neutral recovery contract. It never starts or resumes Claude.
type ClaudeReader struct {
	transcripts *claudestore.TranscriptStore
}

func NewClaude(transcripts *claudestore.TranscriptStore) (*ClaudeReader, error) {
	if transcripts == nil {
		return nil, ErrUnavailable
	}
	return &ClaudeReader{transcripts: transcripts}, nil
}

func (reader *ClaudeReader) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	binding := request.Binding
	if reader == nil || reader.transcripts == nil || ctx == nil || strings.TrimSpace(string(request.SessionID)) == "" ||
		request.Provider != domain.ProviderClaude || binding.Provider != domain.ProviderClaude ||
		binding.SessionID == "" || binding.Generation == 0 || !utf8.ValidString(binding.SessionID) || strings.ContainsRune(binding.SessionID, '\x00') ||
		!filepath.IsAbs(request.Workdir) || !utf8.ValidString(request.Workdir) || strings.ContainsRune(request.Workdir, '\x00') {
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	turns, err := reader.transcripts.AcceptedTurns(ctx, binding.SessionID, request.Workdir)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return sessionruntime.AcceptedTurnReconciliation{}, contextErr
		}
		return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
	}
	result := sessionruntime.AcceptedTurnReconciliation{Turns: make([]sessionruntime.ReconciledAcceptedTurn, len(turns))}
	for index, turn := range turns {
		outcome := sessionruntime.AcceptedTurnUnknown
		if turn.Outcome == claudestore.AcceptedTurnCompleted {
			outcome = sessionruntime.AcceptedTurnCompleted
		} else if turn.Outcome != claudestore.AcceptedTurnUnknown {
			return sessionruntime.AcceptedTurnReconciliation{}, ErrUnavailable
		}
		result.Turns[index] = sessionruntime.ReconciledAcceptedTurn{MessageID: turn.MessageID, Outcome: outcome}
	}
	return result, nil
}

var _ sessionruntime.AcceptedTurnReader = (*ClaudeReader)(nil)
