package clusterupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadProgressPublishesMonotonicObservedStatus(t *testing.T) {
	request := Request{NodeID: "node", UpdateID: "update", Version: "v2"}
	manager := &Manager{status: Status{
		NodeID: "node", UpdateID: request.UpdateID, Version: request.Version,
		Phase: PhaseDownloading, Progress: 8, StartedAt: time.Now(),
	}}
	progress := &downloadProgress{manager: manager, request: request, total: 100, last: -1}
	if _, err := progress.Write(make([]byte, 50)); err != nil {
		t.Fatal(err)
	}
	if manager.status.Phase != PhaseDownloading || manager.status.Progress != 31 ||
		manager.status.BytesDone != 50 || manager.status.BytesTotal != 100 {
		t.Fatalf("status=%#v", manager.status)
	}
	manager.setStatus(request, PhaseVerifying, 60, 100, 100)
	manager.setStatus(request, PhaseDownloading, 20, 60, 100)
	if manager.status.Progress != 60 {
		t.Fatalf("progress regressed: %#v", manager.status)
	}
}

func TestConfirmInstalledPublishesHealthyStatusAfterRestart(t *testing.T) {
	root := t.TempDir()
	request := Request{NodeID: "node", UpdateID: "update", Version: "v2"}
	pendingPath := filepath.Join(root, "update-pending.json")
	if err := writePending(pendingPath, pendingUpdate{
		NodeID: request.NodeID, UpdateID: request.UpdateID, Version: request.Version,
		Previous: filepath.Join(root, "previous"), CurrentLink: filepath.Join(root, "current"),
	}); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		config: ManagerConfig{NodeID: request.NodeID, InstallRoot: root},
		status: Status{
			NodeID: request.NodeID, UpdateID: request.UpdateID, Version: request.Version,
			Phase: PhaseRestarting, Progress: 95,
		},
	}
	if err := manager.ConfirmInstalled(request.Version); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(nil, request)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseHealthy || status.Progress != 100 || status.UpdatedAt.IsZero() {
		t.Fatalf("confirmed status=%#v", status)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending rollback was not disarmed: %v", err)
	}
}
