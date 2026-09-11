package cardtranscript

// Snapshot joins the rendered transcript with its persisted activity clock.
type Snapshot struct {
	Blocks            []Block
	LastEventUnixNano int64
}
