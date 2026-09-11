// Package sessiondeliverygate serializes session selection changes with the
// physical Telegram mutation that publishes the corresponding card.
package sessiondeliverygate

import (
	"sync"

	"bria/internal/domain"
)

type Gate struct{ sessions sync.Map }

func (gate *Gate) Lock(sessionID domain.SessionID) func() {
	value, _ := gate.sessions.LoadOrStore(sessionID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}
