package main

import (
	"sort"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

func localTranscriptWarmRequests(state *domain.State, nodeID string) []transcript.Request {
	if state == nil {
		return nil
	}
	seen := make(map[string]bool)
	result := make([]transcript.Request, 0)
	appendSession := func(ref domain.SessionRef) {
		session, ok := state.Sessions[ref.Key()]
		if !ok || string(session.NodeID) != nodeID || !session.IsLive() ||
			!strings.EqualFold(session.Backend, string(transcript.BackendCodex)) ||
			session.ProviderSessionID == "" || session.Workdir == "" || seen[ref.Key()] {
			return
		}
		seen[ref.Key()] = true
		result = append(result, transcript.Request{
			Backend: transcript.BackendCodex, ProviderSessionID: session.ProviderSessionID,
			Workdir: session.Workdir,
		})
	}
	users := make([]int, 0, len(state.TelegramResponseCards))
	for userID := range state.TelegramResponseCards {
		users = append(users, int(userID))
	}
	sort.Ints(users)
	for _, userID := range users {
		appendSession(state.TelegramResponseCards[domain.UserID(userID)].Session)
	}
	sessions := make([]domain.Session, 0, len(state.Sessions))
	for _, session := range state.Sessions {
		if string(session.NodeID) == nodeID && session.IsLive() {
			sessions = append(sessions, session)
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastEventAt.After(sessions[j].LastEventAt)
	})
	for _, session := range sessions {
		appendSession(session.Ref())
	}
	return result
}
