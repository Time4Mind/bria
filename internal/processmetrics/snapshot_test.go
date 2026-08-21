package processmetrics

import (
	"context"
	"testing"
	"time"
)

func TestCaptureAlwaysReportsRuntimeAndBoundedPlatformValues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snapshot := Capture(ctx)
	if snapshot.Goroutines <= 0 || snapshot.Collection < 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if snapshot.RSSAvailable && snapshot.RSSBytes < 0 {
		t.Fatalf("negative RSS: %+v", snapshot)
	}
	if snapshot.FDsAvailable && snapshot.OpenFDs < 0 {
		t.Fatalf("negative FD count: %+v", snapshot)
	}
	if snapshot.ChildrenAvailable && snapshot.DirectChildren < 0 {
		t.Fatalf("negative child count: %+v", snapshot)
	}
}
