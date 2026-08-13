package inbound

import (
	"context"
	"time"
)

func convertToWAV(
	ctx context.Context,
	runner CommandRunner,
	binary string,
	timeout time.Duration,
	input string,
	output string,
) error {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout := &truncatingBuffer{limit: 4096}
	stderr := &truncatingBuffer{limit: 4096}
	err := runner.Run(commandCtx, stdout, stderr, binary,
		"-nostdin", "-loglevel", "error", "-y", "-i", input,
		"-ar", "16000", "-ac", "1", "-f", "wav", output,
	)
	if err != nil {
		return commandFailure("ffmpeg", err, stderr.String())
	}
	return nil
}
