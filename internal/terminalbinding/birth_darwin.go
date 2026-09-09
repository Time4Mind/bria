//go:build darwin

package terminalbinding

import (
	"context"
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

// ProcessBirth reads kern.proc.pid's extern_proc prefix: timeval start seconds
// and microseconds. Both supported Darwin 64-bit ABIs have this same prefix.
// No process content, command line or environment is inspected.
func ProcessBirth(ctx context.Context, pid int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if pid < 2 {
		return "", ErrMismatch
	}
	mib := [4]int32{1, 14, 1, int32(pid)} // CTL_KERN, KERN_PROC, KERN_PROC_PID.
	var data [4096]byte
	size := uintptr(len(data))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 4,
		uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
	if errno != 0 || size < 16 || size > uintptr(len(data)) {
		return "", ErrUnavailable
	}
	seconds, micros := binary.LittleEndian.Uint64(data[:8]), binary.LittleEndian.Uint32(data[8:12])
	if seconds == 0 || micros >= 1000000 {
		return "", ErrMismatch
	}
	return fmt.Sprintf("%d:%06d", seconds, micros), nil
}
