package main

import (
	"testing"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/runnerhost"
)

func TestValidateRunnerIsolationProfiles(t *testing.T) {
	control := runnerhost.Inspect{OS: "linux", UID: 1000, MountNamespace: "mnt:[1]"}
	tests := []struct {
		name   string
		mode   string
		runner runnerhost.Inspect
		ok     bool
	}{
		{"docker", config.RunnerModeDocker, runnerhost.Inspect{OS: "linux", UID: 10001, Container: true, MountNamespace: "mnt:[2]"}, true},
		{"native", config.RunnerModeNativeUser, runnerhost.Inspect{OS: "linux", UID: 10001}, true},
		{"safe wsl", config.RunnerModeWSL, runnerhost.Inspect{OS: "linux", UID: 10001, WSL: true}, true},
		{"root", config.RunnerModeNativeUser, runnerhost.Inspect{OS: "linux", UID: 0}, false},
		{"same user", config.RunnerModeNativeUser, runnerhost.Inspect{OS: "linux", UID: 1000}, false},
		{"fake docker", config.RunnerModeDocker, runnerhost.Inspect{OS: "linux", UID: 10001}, false},
		{"shared mounts", config.RunnerModeDocker, runnerhost.Inspect{OS: "linux", UID: 10001, Container: true, MountNamespace: "mnt:[1]"}, false},
		{"unsafe wsl interop", config.RunnerModeWSL, runnerhost.Inspect{OS: "linux", UID: 10001, WSL: true, WindowsInterop: true}, false},
		{"unsafe wsl drives", config.RunnerModeWSL, runnerhost.Inspect{OS: "linux", UID: 10001, WSL: true, WindowsMounts: true}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRunnerIsolation(test.mode, control, test.runner)
			if (err == nil) != test.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
