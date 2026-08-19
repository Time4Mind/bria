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
		turn, ok := transcript.LatestCompletedTurn(
			events, transcript.Backend(session.Backend),
		)
		if !ok || turn.FinalAt.Before(session.LastEventAt) {
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

func collectTranscriptRuntime(
	ctx context.Context,
	nodeID domain.NodeID,
	state *domain.State,
	reader transcriptEventReader,
) []domain.TranscriptRuntimeReport {
	if state == nil || reader == nil {
		return nil
	}
	reports := make([]domain.TranscriptRuntimeReport, 0)
	for _, session := range state.Sessions {
		if session.NodeID != nodeID || !session.IsLive() || session.ProviderSessionID == "" ||
			session.RuntimePhase != domain.RuntimeIdle {
			continue
		}
		events, err := reader.Read(ctx, transcript.Request{
			Backend: transcript.Backend(session.Backend), ProviderSessionID: session.ProviderSessionID,
			Workdir: session.Workdir,
		})
		if err != nil {
			continue
		}
		observation, ok := transcript.LatestRuntimeObservation(
			events, transcript.Backend(session.Backend),
		)
		if !ok || observation.At.Before(session.LastEventAt) {
			continue
		}
		if !observation.Running {
			continue
		}
		reports = append(reports, domain.TranscriptRuntimeReport{
			SessionID: session.ID, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeRunning, Timestamp: observation.At,
		})
		if len(reports) == 512 {
			break
		}
	}
	return reports
}

func transcriptRuntimeHeartbeatEnabled(state *domain.State, leaderID string, version string) bool {
	if state == nil || leaderID == "" || version == "" {
		return false
	}
	leader, exists := state.Nodes[domain.NodeID(leaderID)]
	return exists && leader.Version == version
}

func transcriptEventDigest(event transcript.Event) string {
	encoded, _ := json.Marshal(event)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
