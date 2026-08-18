package clusterupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

func (m *Manager) loadStatus() {
	data, err := os.ReadFile(filepath.Join(m.config.InstallRoot, "update-status.json"))
	if err == nil {
		var status Status
		if json.Unmarshal(data, &status) == nil && status.Phase == PhaseFailed {
			status.NodeID = m.config.NodeID
			m.status = status
			return
		}
	}
	pendingData, pendingErr := os.ReadFile(filepath.Join(m.config.InstallRoot, "update-pending.json"))
	if pendingErr == nil {
		var pending pendingUpdate
		if json.Unmarshal(pendingData, &pending) == nil && pending.UpdateID != "" {
			now := time.Now().UTC()
			m.status = Status{
				NodeID: m.config.NodeID, UpdateID: pending.UpdateID, Version: pending.Version,
				Phase: PhaseRestarting, Progress: 95, StartedAt: now, UpdatedAt: now,
			}
		}
	}
}

func activeManagerPhase(phase Phase) bool {
	switch phase {
	case PhaseInspecting, PhaseDownloading, PhaseVerifying, PhaseExtracting,
		PhasePreflight, PhaseActivating, PhaseRestarting, PhaseStaged:
		return true
	default:
		return false
	}
}

func (m *Manager) setStatus(
	request Request, phase Phase, progress int, bytesDone, bytesTotal int64,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.UpdateID != request.UpdateID {
		return
	}
	if progress < m.status.Progress {
		progress = m.status.Progress
	}
	m.status.Phase, m.status.Progress = phase, min(progress, 99)
	m.status.BytesDone, m.status.BytesTotal = bytesDone, bytesTotal
	m.status.UpdatedAt = time.Now().UTC()
}
