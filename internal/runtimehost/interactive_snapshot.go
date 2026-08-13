package runtimehost

import (
	"context"
	"sort"
	"sync"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/interactive"
)

const (
	maxInteractiveSnapshotSessions = 512
	interactiveCaptureWorkers      = 8
)

type promptCapture struct {
	index  int
	report domain.InteractivePromptReport
}

// InteractiveSnapshot scans attached local runtimes concurrently. It returns
// only bounded prompt metadata; captured terminal text never leaves this node.
func (e *LocalExecutor) InteractiveSnapshot(ctx context.Context) []domain.InteractivePromptReport {
	bindings := e.snapshotBindings()
	results := make(chan promptCapture, len(bindings))
	semaphore := make(chan struct{}, interactiveCaptureWorkers)
	var workers sync.WaitGroup
	for index, binding := range bindings {
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			pane, err := e.driver.CapturePane(ctx, binding.TmuxTarget)
			if err != nil {
				return
			}
			prompt, ok := interactive.Detect(pane)
			report := domain.InteractivePromptReport{
				SessionID: domain.SessionID(binding.SessionID), Generation: binding.Generation,
				Present: ok,
			}
			if ok {
				report.Kind, report.Hash = prompt.Kind, prompt.Hash
			}
			results <- promptCapture{index: index, report: report}
		}()
	}
	workers.Wait()
	close(results)
	captures := make([]promptCapture, 0, len(bindings))
	for result := range results {
		captures = append(captures, result)
	}
	sort.Slice(captures, func(i, j int) bool { return captures[i].index < captures[j].index })
	reports := make([]domain.InteractivePromptReport, 0, len(captures))
	for _, capture := range captures {
		reports = append(reports, capture.report)
	}
	return reports
}

func (e *LocalExecutor) snapshotBindings() []RuntimeBinding {
	e.mu.RLock()
	bindings := make([]RuntimeBinding, 0, len(e.sessions))
	for _, session := range e.sessions {
		binding := session.snapshot()
		if binding.TmuxTarget != "" {
			bindings = append(bindings, binding)
		}
	}
	e.mu.RUnlock()
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].SessionID < bindings[j].SessionID
	})
	if len(bindings) > maxInteractiveSnapshotSessions {
		bindings = bindings[:maxInteractiveSnapshotSessions]
	}
	return bindings
}
