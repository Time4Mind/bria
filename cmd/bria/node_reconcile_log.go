package main

import "github.com/Time4Mind/bria/internal/processlog"

// logPeriodicReconcile reports a repeated background failure once, then only
// logs a changed failure or recovery. This keeps a stuck filesystem/tmux error
// from filling the rotating logs every few seconds.
func logPeriodicReconcile(component string, err error, previous *string) {
	if err == nil {
		if *previous != "" {
			processlog.Servicef("bria %s: recovered", component)
			*previous = ""
		}
		return
	}
	current := err.Error()
	if current == *previous {
		return
	}
	*previous = current
	processlog.Failuref(
		processlog.Critical, processlog.FailureDependency,
		"bria %s: outcome=reconcile_failed", component,
	)
}
