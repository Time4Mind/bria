//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"syscall"
)

func preserveFileOwner(file *os.File, source os.FileInfo) error {
	stat, ok := source.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("unsupported file stat type %T", source.Sys())
	}
	return file.Chown(int(stat.Uid), int(stat.Gid))
}
