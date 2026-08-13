//go:build !linux

package runnerhost

import "runtime"

func localInspect() Inspect {
	return Inspect{ProtocolVersion: ProtocolVersion, OS: runtime.GOOS, Arch: runtime.GOARCH, UID: -1}
}
