// Package runnerhost provides the narrow local boundary between Bria's
// control plane and an untrusted provider runtime.
package runnerhost

import "github.com/Time4Mind/bria/internal/runtimehost"

const (
	ProtocolVersion  = 1
	maxRequestBytes  = 2 << 20
	maxResultBytes   = 2 << 20
	maxResponseBytes = 4 << 20
)

type Inspect struct {
	ProtocolVersion int    `json:"protocol_version"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	UID             int    `json:"uid"`
	Container       bool   `json:"container"`
	WSL             bool   `json:"wsl"`
	WindowsInterop  bool   `json:"windows_interop"`
	WindowsMounts   bool   `json:"windows_mounts"`
	MountNamespace  string `json:"mount_namespace,omitempty"`
}

func LocalInspect() Inspect { return localInspect() }

type commandRequest struct {
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Args       []string `json:"args,omitempty"`
	Input      []byte   `json:"input,omitempty"`
	Initialize []byte   `json:"initialize,omitempty"`
	ExpectedID int      `json:"expected_id,omitempty"`
	TimeoutMS  int64    `json:"timeout_ms"`
}

type commandResponse struct {
	Result runtimehost.CommandResult `json:"result"`
	Error  string                    `json:"error,omitempty"`
}

type pathRequest struct {
	Name string `json:"name"`
}

type pathResponse struct {
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}
