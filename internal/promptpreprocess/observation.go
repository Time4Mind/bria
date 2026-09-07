package promptpreprocess

const (
	StageComplete          = "complete"
	StageCache             = "cache"
	CategorySuccess        = "success"
	CategoryCached         = "cached"
	CategoryCachedFallback = "cached_fallback"
)

// SuccessObservation is emitted by the orchestration caller only after both
// Process and ValidateResult succeed. ModelEvidence is empty when the provider
// does not expose an execution receipt; Model alone is not confirmation.
func SuccessObservation(request Request, result Result) Observation {
	return Observation{
		ComputerID: request.ComputerID, SessionID: request.SessionID, MessageID: request.MessageID,
		Provider: result.Provider, Model: result.Model, ModelEvidence: result.ModelEvidence,
		Stage: StageComplete, Category: CategorySuccess, Attempts: 1,
	}
}

// CachedObservation means the durable prepared envelope was reused: no new
// invocation occurred. The envelope contains no provider/model provenance, so
// this event deliberately cannot claim an execution model or a fresh success.
func CachedObservation(request Request, failed bool) Observation {
	category := CategoryCached
	if failed {
		category = CategoryCachedFallback
	}
	return Observation{
		ComputerID: request.ComputerID, SessionID: request.SessionID, MessageID: request.MessageID,
		Stage: StageCache, Category: category, Attempts: 0,
	}
}
