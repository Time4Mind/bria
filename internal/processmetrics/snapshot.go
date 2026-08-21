package processmetrics

import (
	"context"
	"runtime"
	"time"
)

type Snapshot struct {
	RSSBytes          int64
	RSSAvailable      bool
	Goroutines        int
	OpenFDs           int
	FDsAvailable      bool
	FDsCapped         bool
	DirectChildren    int
	ChildrenAvailable bool
	ChildrenCapped    bool
	Collection        time.Duration
}

func Capture(ctx context.Context) Snapshot {
	startedAt := time.Now()
	snapshot := capturePlatform(ctx)
	snapshot.Goroutines = runtime.NumGoroutine()
	snapshot.Collection = time.Since(startedAt)
	return snapshot
}

func (snapshot Snapshot) Complete() bool {
	return snapshot.RSSAvailable && snapshot.FDsAvailable && snapshot.ChildrenAvailable &&
		!snapshot.FDsCapped && !snapshot.ChildrenCapped
}
