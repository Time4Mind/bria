package clusterupdate

import (
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
