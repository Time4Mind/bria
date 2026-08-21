//go:build linux

package processmetrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

const (
	maxLinuxFDEntries = 65_536
	maxLinuxTasks     = 256
	maxLinuxChildren  = 4_096
)

func capturePlatform(ctx context.Context) Snapshot {
	snapshot := Snapshot{}
	if data, err := os.ReadFile("/proc/self/statm"); err == nil {
		if bytes, ok := parseRSSPages(string(data), int64(os.Getpagesize())); ok {
			snapshot.RSSBytes = bytes
			snapshot.RSSAvailable = true
		}
	}
	if count, capped, err := countOpenFDs(ctx, "/proc/self/fd", maxLinuxFDEntries); err == nil {
		snapshot.OpenFDs = count
		snapshot.FDsCapped = capped
		snapshot.FDsAvailable = true
	}
	children := make(map[string]struct{})
	tasksCapped := false
	_, tasksCapped, taskErr := walkNumericDirectory(
		ctx, "/proc/self/task", maxLinuxTasks,
		func(task string) error {
			file, err := os.Open(filepath.Join("/proc/self/task", task, "children"))
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			childrenCapped, scanErr := scanChildPIDs(ctx, file, children, maxLinuxChildren)
			closeErr := file.Close()
			if childrenCapped {
				snapshot.ChildrenCapped = true
			}
			if scanErr != nil {
				return scanErr
			}
			return closeErr
		},
	)
	if taskErr == nil {
		snapshot.DirectChildren = len(children)
		snapshot.ChildrenCapped = snapshot.ChildrenCapped || tasksCapped
		snapshot.ChildrenAvailable = true
	}
	return snapshot
}
