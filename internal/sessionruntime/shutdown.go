package sessionruntime

import (
	"context"
	"errors"
	"sync"
)

// Shutdown preserves session storage while closing and reaping every adapter
// owned by this runtime. Process exit must be awaited before the bot exits;
// closing inherited stdin alone is not a synchronous cleanup contract.
func (starter *Starter) Shutdown(ctx context.Context) error {
	starter.launchMu.Lock()
	defer starter.launchMu.Unlock()
	starter.shuttingDown = true
	starter.mu.Lock()
	records := make([]*processRecord, 0, len(starter.processes))
	for _, record := range starter.processes {
		records = append(records, record)
	}
	starter.mu.Unlock()
	var wait sync.WaitGroup
	errorsOut := make(chan error, len(records))
	for _, record := range records {
		wait.Add(1)
		go func(record *processRecord) {
			defer wait.Done()
			mode := "close"
			if record.persistentTerminal {
				mode = "detach"
			}
			errorsOut <- starter.release(ctx, record.request, record.binding, mode)
		}(record)
	}
	wait.Wait()
	close(errorsOut)
	var result error
	for err := range errorsOut {
		result = errors.Join(result, err)
	}
	return result
}
