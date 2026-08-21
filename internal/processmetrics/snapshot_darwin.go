//go:build darwin

package processmetrics

import (
	"context"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/unix"
)

const (
	maxDarwinFDEntries       = 65_536
	maxDarwinChildren        = 4_096
	maxDarwinProcessTableCap = 16_384
)

func capturePlatform(ctx context.Context) Snapshot {
	snapshot := Snapshot{}
	pid := os.Getpid()
	if maxProcesses, err := unix.SysctlUint32("kern.maxproc"); ctx.Err() == nil && err == nil &&
		maxProcesses <= maxDarwinProcessTableCap {
		if processes, processErr := unix.SysctlKinfoProcSlice("kern.proc.all"); processErr == nil {
			children := 0
			for _, process := range processes {
				if process.Eproc.Ppid == int32(pid) {
					children++
				}
			}
			snapshot.DirectChildren = min(children, maxDarwinChildren)
			snapshot.ChildrenCapped = children > maxDarwinChildren
			snapshot.ChildrenAvailable = ctx.Err() == nil
		}
	}
	if count, capped, err := countOpenFDs(ctx, "/dev/fd", maxDarwinFDEntries); err == nil {
		snapshot.OpenFDs = count
		snapshot.FDsCapped = capped
		snapshot.FDsAvailable = true
	}
	if ctx.Err() != nil {
		return snapshot
	}
	command := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(pid))
	if output, err := command.Output(); err == nil {
		if bytes, ok := parseRSSKibibytes(string(output)); ok {
			snapshot.RSSBytes = bytes
			snapshot.RSSAvailable = true
		}
	}
	return snapshot
}
