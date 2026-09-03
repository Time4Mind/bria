//go:build windows

package coordinatorstate

import "os"

// Windows ownership is enforced by ACL; portable FileInfo does not expose the SID.
func ownedByCurrentUser(os.FileInfo) bool { return true }
