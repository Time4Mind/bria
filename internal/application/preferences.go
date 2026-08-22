package application

import "github.com/Time4Mind/bria/internal/domain"

// Preferences returns the actor's replicated interaction preferences without
// imposing any transport-specific projection.
func (s *Service) Preferences(actor Principal) (domain.UserPreferences, error) {
	if actor.UserID <= 0 {
		return domain.UserPreferences{}, domain.ErrAccessDenied
	}
	state := s.reader.State()
	preferences, ok := state.Preferences[actor.UserID]
	if !ok {
		return domain.UserPreferences{}, domain.ErrAccessDenied
	}
	return preferences, nil
}
