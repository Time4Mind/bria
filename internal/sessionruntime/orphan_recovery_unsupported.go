//go:build !linux

package sessionruntime

import "bria/internal/app"

// Other platforms do not expose a portable, safe process-table API. Their
// normal adapter ownership and restart path remains unchanged.
func cleanupOrphanResumeProcess(app.StartSessionRequest) error { return nil }
