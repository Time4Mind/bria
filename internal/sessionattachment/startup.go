package sessionattachment

import (
	"bria/internal/domain"
	"bria/internal/providerattachport"
	"errors"
)

// RecordPersistedExits shares the established startup boundary with both
// supervised recovery and the safe no-history fallback. It records only exact
// local bindings; it never turns a later unknown Abort into a blanket success.
func RecordPersistedExits(computer domain.ComputerID, sessions []domain.Session, starter any, recoverable func(domain.SessionStatus) bool) error {
	if recoverable == nil {
		return errors.New("startup recovery predicate is required")
	}
	recorder, ok := starter.(interface {
		ConfirmPersistedExit(providerattachport.StartSessionRequest, domain.ProviderBinding) error
	})
	if !ok {
		return nil
	}
	for _, session := range sessions {
		if attacher, ok := starter.(providerattachport.SessionAttacher); ok && attacher.SupportsAttach(session.Provider()) {
			continue
		}
		binding, bound := session.Binding()
		if session.ComputerID() != computer || !bound || !recoverable(session.Status()) {
			continue
		}
		request := providerattachport.StartSessionRequest{
			SessionID: session.ID(), ComputerID: session.ComputerID(), Provider: session.Provider(), Workdir: session.Workdir(),
			Mode: providerattachport.SessionStartResume, PriorBinding: &binding,
		}
		if err := recorder.ConfirmPersistedExit(request, binding); err != nil {
			return err
		}
	}
	return nil
}
