package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type transcriptEventReader interface {
	Read(context.Context, transcript.Request) ([]transcript.Event, error)
}

func collectTranscriptFinals(
	ctx context.Context,
	nodeID domain.NodeID,
	state *domain.State,
	reader transcriptEventReader,
) []domain.TranscriptFinalReport {
	if state == nil || reader == nil {
		return nil
	}
	reports := make([]domain.TranscriptFinalReport, 0)
	for _, session := range state.Sessions {
		if session.NodeID != nodeID || !session.IsLive() ||
			session.RuntimePhase != domain.RuntimeRunning || session.ProviderSessionID == "" {
			continue
		}
		events, err := reader.Read(ctx, transcript.Request{
			Backend:           transcript.Backend(session.Backend),
			ProviderSessionID: session.ProviderSessionID, Workdir: session.Workdir,
		})
		if err != nil {
			if errors.Is(err, transcript.ErrTranscriptNotFound) {
				continue
			}
			continue
		}
		turn, ok := transcript.LatestCompletedTurn(events)
		if !ok || turn.FinalAt.Before(session.LastEventAt) ||
			(turn.HasUser && turn.UserAt.Before(session.LastEventAt)) {
			continue
		}
		reports = append(reports, domain.TranscriptFinalReport{
			SessionID: session.ID, Generation: session.RuntimeGeneration,
			Timestamp: turn.FinalAt, Digest: transcriptEventDigest(turn.Final),
		})
		if len(reports) == 512 {
			break
		}
	}
	return reports
}

func transcriptEventDigest(event transcript.Event) string {
	encoded, _ := json.Marshal(event)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
