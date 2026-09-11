package runtimediagnostic

import (
	"io"
	"time"
)

// DrainBeforeWait gives a diagnostic pipe a bounded chance to close naturally,
// stops the process tree, and joins the reader before exec.Cmd.Wait can close
// the pipe. A stuck inherited descriptor is closed after the second bound.
func DrainBeforeWait(done <-chan struct{}, stream io.Closer, stop func(), grace time.Duration) {
	if done == nil || stream == nil || stop == nil || grace <= 0 {
		if stop != nil {
			stop()
		}
		return
	}
	drained := wait(done, grace)
	stop()
	if drained || wait(done, grace) {
		return
	}
	_ = stream.Close()
	<-done
}

func wait(done <-chan struct{}, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
