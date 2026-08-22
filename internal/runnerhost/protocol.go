// Package runnerhost provides the narrow local boundary between Bria's
// control plane and an untrusted provider runtime.
package runnerhost

import (
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

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

type bindingLookupRequest struct {
	Ref     domain.SessionRef `json:"ref"`
	Workdir string            `json:"workdir,omitempty"`
}

type bindingLookupResponse struct {
	Record providerbinding.Record `json:"record"`
	Found  bool                   `json:"found"`
	Error  string                 `json:"error,omitempty"`
}

type bindingSnapshotResponse struct {
	Records []providerbinding.Record `json:"records"`
	Error   string                   `json:"error,omitempty"`
}

type bindingSweepRequest struct {
	Input providerbinding.SweepInput `json:"input"`
}

type bindingDeleteRequest struct {
	Ref        domain.SessionRef `json:"ref"`
	Generation uint64            `json:"generation"`
}

type bindingMutationResponse struct {
	Error string `json:"error,omitempty"`
}

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
