package consensus

import (
	"io"
	"log"
	"time"

	"github.com/hashicorp/go-hclog"
)

func newRaftLogger(output io.Writer) hclog.Logger {
	if output == nil {
		output = io.Discard
	}
	return hclog.New(&hclog.LoggerOptions{
		Name:   "raft",
		Level:  hclog.Warn,
		Output: newRaftLogRateLimiter(output, 30*time.Second, time.Now),
	})
}

// Retain a standard logger adapter for integrations which still require one.
func standardLogger(output io.Writer) *log.Logger {
	if output == nil {
		output = io.Discard
	}
	return log.New(output, "raft: ", log.LstdFlags|log.LUTC)
}
