package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

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
		event, timestamp, ok := finalTranscriptEvent(events)
		if !ok || timestamp.Before(session.LastEventAt) {
			continue
		}
		reports = append(reports, domain.TranscriptFinalReport{
			SessionID: session.ID, Generation: session.RuntimeGeneration,
			Timestamp: timestamp, Digest: transcriptEventDigest(event),
		})
		if len(reports) == 512 {
			break
		}
	}
	return reports
}

func finalTranscriptEvent(events []transcript.Event) (transcript.Event, time.Time, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Kind != transcript.EventAssistantFinal {
			if event.Kind == transcript.EventAssistantText || event.Kind == transcript.EventThinking ||
				event.Kind == transcript.EventToolCall {
				return transcript.Event{}, time.Time{}, false
			}
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			return transcript.Event{}, time.Time{}, false
		}
		return event, timestamp.UTC(), true
	}
	return transcript.Event{}, time.Time{}, false
}

func transcriptEventDigest(event transcript.Event) string {
	encoded, _ := json.Marshal(event)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
