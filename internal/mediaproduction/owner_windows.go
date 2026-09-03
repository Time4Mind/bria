//go:build windows

package mediaproduction

import "os"

func ownedByCurrentUser(os.FileInfo) bool { return true }
