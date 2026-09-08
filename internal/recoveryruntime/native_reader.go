package recoveryruntime

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/nativereceiptstore"
	"bria/internal/sessionruntime"
)

// NativeReader reads the adapter's private exact-session acceptance receipts.
// It never launches a provider or interprets native transcript prompt text.
type NativeReader struct {
	root           string
	provider       domain.Provider
	transcriptRoot string
}

func NewNative(root string, provider domain.Provider) (*NativeReader, error) {
	if !filepath.IsAbs(root) || !utf8.ValidString(root) || strings.ContainsRune(root, 0) || provider != domain.ProviderCodex && provider != domain.ProviderClaude {
		return nil, ErrUnavailable
	}
	return &NativeReader{root: filepath.Clean(root), provider: provider}, nil
}

// NewNativeWithTranscriptRoot optionally proves Codex finals from the owning
// composition's explicit native sessions root. It never launches a provider.
func NewNativeWithTranscriptRoot(root string, provider domain.Provider, transcriptRoot string) (*NativeReader, error) {
	reader, err := NewNative(root, provider)
	if err != nil {
		return nil, err
	}
	if transcriptRoot != "" && (provider != domain.ProviderCodex || !filepath.IsAbs(transcriptRoot) || !utf8.ValidString(transcriptRoot) || strings.ContainsRune(transcriptRoot, 0)) {
		return nil, ErrUnavailable
	}
	reader.transcriptRoot = transcriptRoot
	return reader, nil
}

func (reader *NativeReader) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	unavailable := sessionruntime.AcceptedTurnReconciliation{}
	if reader == nil || ctx == nil || request.Provider != reader.provider || request.Binding.Provider != reader.provider || request.SessionID == "" || request.Binding.Generation == 0 || !nativeUUID(request.Binding.SessionID) || !filepath.IsAbs(request.Workdir) || strings.ContainsRune(request.Workdir, 0) {
		return unavailable, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return unavailable, err
	}
	doc, err := nativereceiptstore.Read(reader.root, request.Binding.SessionID)
	if err != nil {
		return unavailable, ErrUnavailable
	}
	turns := make([]sessionruntime.ReconciledAcceptedTurn, 0, len(doc.Receipts))
	for message, outcome := range doc.Receipts {
		turns = append(turns, sessionruntime.ReconciledAcceptedTurn{MessageID: message, Outcome: sessionruntime.AcceptedTurnOutcome(outcome), TurnID: doc.TurnIDs[message]})
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i].MessageID < turns[j].MessageID })
	if err := ctx.Err(); err != nil {
		return unavailable, err
	}
	if err := reader.enrichNativeTurns(ctx, request, turns); err != nil {
		return unavailable, err
	}
	return sessionruntime.AcceptedTurnReconciliation{Turns: turns}, nil
}

func nativeUUID(id string) bool {
	if len(id) != 36 || id != strings.ToLower(id) || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(decoded) == 16
}

var _ sessionruntime.AcceptedTurnReader = (*NativeReader)(nil)
