//go:build !darwin && !linux

package processmetrics

import "context"

func capturePlatform(context.Context) Snapshot { return Snapshot{} }
