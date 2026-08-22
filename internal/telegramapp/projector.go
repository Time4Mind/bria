package telegramapp

import (
	"errors"
	"hash/fnv"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type Principal = application.Principal
type StateReader = application.StateReader

type ProjectionTokens interface {
	Node(domain.UserID, telegramui.Action, domain.NodeID) (telegramui.OpaqueToken, error)
	Session(domain.UserID, telegramui.Action, domain.SessionRef) (telegramui.OpaqueToken, error)
	Choice(domain.UserID, telegramui.Action, string, string) (telegramui.OpaqueToken, error)
}

type pageProjectionTokens interface {
	Page(domain.UserID, telegramui.Action, domain.SessionRef, int) (telegramui.OpaqueToken, error)
}

type archiveProjectionTokens interface {
	Archive(domain.UserID, telegramui.Action, domain.SessionRef, int) (telegramui.OpaqueToken, error)
}

type TelegramProjector struct {
	reader  StateReader
	tokens  ProjectionTokens
	leaders interface{ LeaderID() string }
}

func (p *TelegramProjector) SetLeadership(leaders interface{ LeaderID() string }) {
	p.leaders = leaders
}

func NewTelegramProjector(
	reader StateReader,
	tokens ProjectionTokens,
) (*TelegramProjector, error) {
	if reader == nil || tokens == nil {
		return nil, errors.New("state reader and projection tokens are required")
	}
	return &TelegramProjector{reader: reader, tokens: tokens}, nil
}

func (p *TelegramProjector) MainMenu(actor Principal) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	name := ""
	nodeID := state.Navigation.ActiveNodeByUser[actor.UserID]
	sessionID := state.Navigation.ActiveSessionByUserNode[actor.UserID][nodeID]
	ref := domain.SessionRef{NodeID: nodeID, SessionID: sessionID}
	if node, ok := state.Nodes[nodeID]; ok && nodeAvailable(node.Status) &&
		state.CanViewSession(actor.UserID, ref) {
		session := state.Sessions[ref.Key()]
		if session.IsLive() {
			name = session.Name
		}
	}
	return telegramui.RenderMainMenu(actorCopy(state, actor), name), nil
}

func actorCopy(state *domain.State, actor Principal) i18n.Localizer {
	preferences, ok := state.Preferences[actor.UserID]
	if !ok {
		preferences = domain.DefaultUserPreferences()
	}
	return i18n.For(string(preferences.EffectiveLanguage()))
}

func (p *TelegramProjector) actorState(actor Principal) (*domain.State, error) {
	if actor.UserID <= 0 {
		return nil, domain.ErrAccessDenied
	}
	state := p.reader.State()
	if state == nil {
		return nil, errors.New("state reader returned nil")
	}
	if _, ok := state.Users[actor.UserID]; !ok {
		return nil, domain.ErrAccessDenied
	}
	return state, nil
}

func visibleNodes(state *domain.State, actor Principal, leaderIDs ...domain.NodeID) []domain.Node {
	return application.VisibleNodes(state, actor, leaderIDs...)
}

func projectionNodeStatus(status domain.NodeStatus) telegramui.NodeStatus {
	switch status {
	case domain.NodeOnline:
		return telegramui.NodeOnline
	case domain.NodeReconnecting:
		return telegramui.NodeReconnecting
	default:
		return telegramui.NodeOffline
	}
}

func nodeAvailable(status domain.NodeStatus) bool {
	return status == domain.NodeOnline || status == domain.NodeReconnecting
}

func nodeMarker(nodeID domain.NodeID) string {
	markers := [...]string{"🟦", "🟩", "🟨", "🟧", "🟪", "🟥"}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(nodeID))
	return markers[hash.Sum32()%uint32(len(markers))]
}
