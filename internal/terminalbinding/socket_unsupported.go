//go:build !linux && !darwin

package terminalbinding

import (
	"context"
	"os"
)

func private(os.FileInfo, bool) bool                    { return false }
func trustedDirectory(os.FileInfo) bool                 { return false }
func SocketIdentity(string) (uint64, uint64, error)     { return 0, 0, ErrUnavailable }
func ProcessBirth(context.Context, int) (string, error) { return "", ErrUnavailable }
