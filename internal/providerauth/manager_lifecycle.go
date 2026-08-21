package providerauth

import "time"

func (m *Manager) markTerminalLocked(flowID string, entry *flow, state State, detail string) {
	entry.state, entry.detail = state, detail
	entry.terminalAt = time.Now()
	terminalAt := entry.terminalAt
	go m.expireTerminal(flowID, entry, terminalAt)
}

func (m *Manager) expireTerminal(flowID string, entry *flow, terminalAt time.Time) {
	timer := time.NewTimer(m.terminalRetention())
	defer timer.Stop()
	<-timer.C
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.flows[flowID]
	if !exists || current != entry || !entry.state.Terminal() || entry.terminalAt != terminalAt {
		return
	}
	delete(m.flows, flowID)
}

func (m *Manager) terminalRetention() time.Duration {
	retention := terminalObservationTTL
	if m.ttl > 0 && m.ttl < retention {
		retention = m.ttl
	}
	return retention
}

func (m *Manager) expire(flowID string, entry *flow) {
	timer := time.NewTimer(time.Until(entry.expiresAt))
	defer timer.Stop()
	<-timer.C
	m.mu.Lock()
	current, exists := m.flows[flowID]
	if !exists || current != entry {
		m.mu.Unlock()
		return
	}
	if entry.state.Terminal() {
		// Terminal flows have their own observation timer. Do not let the
		// original pending-flow expiry shorten that window when completion
		// happens close to the original deadline.
		m.mu.Unlock()
		return
	}
	entry.state = StateCancelled
	delete(m.flows, flowID)
	m.mu.Unlock()
	_ = entry.process.Cancel()
}

func (m *Manager) authorizedFlowLocked(actorID int64, nodeID, flowID string) (*flow, bool) {
	entry, ok := m.flows[flowID]
	if !ok || entry.actorID != actorID || entry.nodeID != nodeID {
		return nil, false
	}
	now := time.Now()
	if entry.state.Terminal() {
		if !now.Before(entry.terminalAt.Add(m.terminalRetention())) {
			delete(m.flows, flowID)
			return nil, false
		}
		return entry, true
	}
	if !now.Before(entry.expiresAt) {
		delete(m.flows, flowID)
		entry.state = StateCancelled
		go entry.process.Cancel()
		return nil, false
	}
	return entry, true
}

func (m *Manager) cancelMatchingLocked(actorID int64, backend string) {
	for id, entry := range m.flows {
		if entry.actorID == actorID && entry.backend == backend && !entry.state.Terminal() {
			entry.state = StateCancelled
			delete(m.flows, id)
			go entry.process.Cancel()
		}
	}
}
