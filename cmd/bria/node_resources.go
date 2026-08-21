package main

import (
	"context"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/processmetrics"
)

const (
	resourceSampleInterval = 5 * time.Minute
	resourceSampleTimeout  = 2 * time.Second
)

type nodeResourceMonitor struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func startNodeResourceMonitor(parent context.Context) *nodeResourceMonitor {
	ctx, cancel := context.WithCancel(parent)
	monitor := &nodeResourceMonitor{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(monitor.done)
		ticker := time.NewTicker(resourceSampleInterval)
		defer ticker.Stop()
		runNodeResourceMonitor(ctx, ticker.C, processmetrics.Capture, logNodeResourceSnapshot)
	}()
	return monitor
}

func (monitor *nodeResourceMonitor) Close() {
	if monitor == nil {
		return
	}
	monitor.once.Do(monitor.cancel)
	<-monitor.done
}

func runNodeResourceMonitor(
	ctx context.Context,
	ticks <-chan time.Time,
	capture func(context.Context) processmetrics.Snapshot,
	emit func(processmetrics.Snapshot),
) {
	sample := func() {
		sampleCtx, cancel := context.WithTimeout(ctx, resourceSampleTimeout)
		defer cancel()
		emit(capture(sampleCtx))
	}
	sample()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			sample()
		}
	}
}

func logNodeResourceSnapshot(snapshot processmetrics.Snapshot) {
	outcome := "ok"
	if !snapshot.Complete() {
		outcome = "partial"
	}
	format := "bria process_metrics: outcome=%s rss_bytes=%d rss_available=%t goroutines=%d open_fds=%d fds_available=%t fds_capped=%t direct_child_processes=%d children_available=%t children_capped=%t collection_ms=%d"
	arguments := []any{
		outcome, snapshot.RSSBytes, snapshot.RSSAvailable, snapshot.Goroutines,
		snapshot.OpenFDs, snapshot.FDsAvailable, snapshot.FDsCapped,
		snapshot.DirectChildren, snapshot.ChildrenAvailable, snapshot.ChildrenCapped,
		snapshot.Collection.Milliseconds(),
	}
	if snapshot.Complete() {
		processlog.Servicef(format, arguments...)
		return
	}
	processlog.Failuref(processlog.Service, processlog.FailureDependency, format, arguments...)
}
