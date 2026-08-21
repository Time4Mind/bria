package clusterstate

import "github.com/Time4Mind/bria/internal/domain"

// ReattachSessionRuntime carries version gates for repairing a false
// resume_failed archive after the origin node proves the runtime still exists.
type ReattachSessionRuntime struct {
	Session            domain.SessionRef `json:"session"`
	ExpectedGeneration uint64            `json:"expected_generation"`
	ExpectedRevision   uint64            `json:"expected_revision"`
}
