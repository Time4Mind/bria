package speechsetup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

func (m *Manager) initialStatus() Status {
	inspected := m.inspect()
	persisted, ok := m.loadStatus()
	if !ok {
		return inspected
	}
	if persisted.Engine != inspected.Engine {
		m.persistStatus(inspected)
		return inspected
	}
	if persisted.Phase == PhaseInstalling {
		if inspected.Phase == PhaseReady {
			return inspected
		}
		persisted.Phase = PhaseMissing
		persisted.Detail = "speech setup was interrupted and can be resumed"
		persisted.UpdatedAt = time.Now()
		m.persistStatus(persisted)
		return persisted
	}
	if persisted.Phase == PhaseFailed || persisted.Phase == PhasePermissionRequired {
		if inspected.Phase != PhaseReady {
			return persisted
		}
	}
	if persisted.Phase == PhaseReady && persisted.Engine == "apple" {
		if _, err := executable(m.applePath()); err == nil {
			return persisted
		}
	}
	return inspected
}

func (m *Manager) statusPath() string {
	return filepath.Join(m.config.DataDir, "speech", "setup-status.json")
}

func (m *Manager) loadStatus() (Status, bool) {
	data, err := os.ReadFile(m.statusPath())
	if err != nil {
		return Status{}, false
	}
	var status Status
	if json.Unmarshal(data, &status) != nil || !status.Validate(m.config.NodeID) {
		return Status{}, false
	}
	return status, true
}

func (m *Manager) persistStatus(status Status) {
	directory := filepath.Dir(m.statusPath())
	if os.MkdirAll(directory, 0o700) != nil {
		return
	}
	data, err := json.Marshal(status)
	if err != nil {
		return
	}
	temporary := m.statusPath() + ".tmp"
	if os.WriteFile(temporary, data, 0o600) != nil {
		return
	}
	if os.Rename(temporary, m.statusPath()) != nil {
		_ = os.Remove(temporary)
	}
}
