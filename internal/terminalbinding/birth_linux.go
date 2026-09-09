//go:build linux

package terminalbinding

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// ProcessBirth binds start ticks to this kernel boot, not a rounded ps date.
func ProcessBirth(ctx context.Context, pid int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if pid < 2 {
		return "", ErrMismatch
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", ErrUnavailable
	}
	end := strings.LastIndexByte(string(data), ')')
	fields := strings.Fields(string(data)[end+1:])
	if end < 0 || len(fields) < 20 || fields[0] == "Z" {
		return "", ErrUnavailable
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", ErrMismatch
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", ErrUnavailable
	}
	return strings.TrimSpace(string(boot)) + ":" + fields[19], nil
}
