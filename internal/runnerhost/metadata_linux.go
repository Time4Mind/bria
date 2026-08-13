//go:build linux

package runnerhost

import (
	"os"
	"runtime"
	"strings"
)

func localInspect() Inspect {
	osRelease, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	mounts, _ := os.ReadFile("/proc/mounts")
	cgroups, _ := os.ReadFile("/proc/1/cgroup")
	_, dockerMarker := os.Stat("/.dockerenv")
	_, interopErr := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop")
	namespace, _ := os.Readlink("/proc/self/ns/mnt")
	lowerRelease := strings.ToLower(string(osRelease))
	lowerMounts := strings.ToLower(string(mounts))
	lowerCgroups := strings.ToLower(string(cgroups))
	return Inspect{
		ProtocolVersion: ProtocolVersion, OS: runtime.GOOS, Arch: runtime.GOARCH,
		UID: os.Geteuid(), Container: dockerMarker == nil ||
			strings.Contains(lowerCgroups, "docker") || strings.Contains(lowerCgroups, "containerd"),
		WSL:            strings.Contains(lowerRelease, "microsoft") || strings.Contains(lowerRelease, "wsl"),
		WindowsInterop: interopErr == nil,
		WindowsMounts: strings.Contains(lowerMounts, " - 9p ") ||
			strings.Contains(lowerMounts, " drvfs ") || strings.Contains(lowerMounts, "/mnt/c "),
		MountNamespace: namespace,
	}
}
