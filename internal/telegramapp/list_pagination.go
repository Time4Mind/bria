package telegramapp

import (
	"strconv"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const maxListCallbackPages = 512

func isListPageAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionNodesPrevious, telegramui.ActionNodesNext,
		telegramui.ActionSessionsPrevious, telegramui.ActionSessionsNext:
		return true
	default:
		return false
	}
}

func (h *Handler) openListPage(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	preferences, err := h.service.Preferences(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	flowID := "nodes-page"
	nodeID := domain.NodeID("")
	if action == telegramui.ActionSessionsPrevious || action == telegramui.ActionSessionsNext {
		if preferences.SessionView == domain.ViewHostFirst {
			selected, ok, selectedErr := h.service.SelectedNode(actor)
			if selectedErr != nil || !ok {
				return telegramui.Screen{}, domain.ErrNotFound
			}
			nodeID = selected.Node.ID
		}
		flowID = "sessions-page-" + string(nodeID)
	}
	values := make([]string, maxListCallbackPages)
	for index := range values {
		values[index] = strconv.Itoa(index + 1)
	}
	value, err := h.tokens.ResolveChoice(actor.UserID, action, flowID, token, values)
	if err != nil {
		return telegramui.Screen{}, err
	}
	page, err := strconv.Atoi(value)
	if err != nil {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	if action == telegramui.ActionNodesPrevious || action == telegramui.ActionNodesNext {
		return h.projector.OpenSessionsPage(actor, page)
	}
	if preferences.SessionView == domain.ViewHostFirst {
		return h.projector.NodeSessionsPage(actor, nodeID, page)
	}
	return h.projector.OpenSessionsPage(actor, page)
}
