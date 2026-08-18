package telegramapp

import (
	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type visibleCardView struct {
	known   bool
	session domain.SessionRef
	epoch   uint64
}

type visibleCardChange struct {
	userID   domain.UserID
	epoch    uint64
	previous visibleCardView
}

func (h *Handler) beginVisibleScreen(
	userID domain.UserID,
	screen telegramui.Screen,
) visibleCardChange {
	ref := domain.SessionRef{}
	if checkpoint := screen.Checkpoint; checkpoint != nil {
		candidate := domain.SessionRef{
			NodeID: domain.NodeID(checkpoint.NodeID), SessionID: domain.SessionID(checkpoint.SessionID),
		}
		if candidate.Validate() == nil {
			ref = candidate
		}
	}
	h.viewMu.Lock()
	defer h.viewMu.Unlock()
	previous := h.visibleCardViews[userID]
	next := visibleCardView{known: true, session: ref, epoch: previous.epoch + 1}
	h.visibleCardViews[userID] = next
	return visibleCardChange{userID: userID, epoch: next.epoch, previous: previous}
}

func (h *Handler) rollbackVisibleScreen(change visibleCardChange) {
	h.viewMu.Lock()
	defer h.viewMu.Unlock()
	current := h.visibleCardViews[change.userID]
	if current.epoch != change.epoch {
		return
	}
	previous := change.previous
	previous.epoch = current.epoch + 1
	h.visibleCardViews[change.userID] = previous
}

func (h *Handler) visibleSessionMatches(
	actor application.Principal,
	ref domain.SessionRef,
) bool {
	h.viewMu.Lock()
	view, known := h.visibleCardViews[actor.UserID]
	h.viewMu.Unlock()
	if known && view.known {
		return view.session == ref
	}
	card, exists, err := h.service.TelegramResponseCard(actor)
	if err != nil || !exists {
		return false
	}
	h.viewMu.Lock()
	view, known = h.visibleCardViews[actor.UserID]
	if !known || !view.known {
		view = visibleCardView{known: true, session: card.Session, epoch: view.epoch + 1}
		h.visibleCardViews[actor.UserID] = view
	}
	h.viewMu.Unlock()
	return view.session == ref
}

func (h *Handler) visibleEpochCurrent(userID domain.UserID, epoch uint64) bool {
	h.viewMu.Lock()
	defer h.viewMu.Unlock()
	view, ok := h.visibleCardViews[userID]
	return ok && view.known && view.epoch == epoch
}
