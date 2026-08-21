package domain

func (s *State) cloneTelegramPresentation(clone *State) {
	for userID, card := range s.TelegramResponseCards {
		clone.TelegramResponseCards[userID] = card
	}
	for userID, views := range s.TelegramSessionViews {
		copyViews := make(map[string]TelegramSessionView, len(views))
		for key, view := range views {
			copyViews[key] = view
		}
		clone.TelegramSessionViews[userID] = copyViews
	}
}
