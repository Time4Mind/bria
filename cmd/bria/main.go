package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/Time4Mind/bria/internal/buildinfo"
	"github.com/Time4Mind/bria/internal/platform"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type versionOutput struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

type probeOutput struct {
	Name         string   `json:"name"`
	Available    bool     `json:"available"`
	Executable   string   `json:"executable,omitempty"`
	Version      string   `json:"version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type doctorOutput struct {
	Version   versionOutput `json:"build"`
	BootID    string        `json:"boot_id,omitempty"`
	BootError string        `json:"boot_error,omitempty"`
	Tmux      probeOutput   `json:"tmux"`
	Backends  []probeOutput `json:"backends"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "version":
		if len(os.Args) != 2 {
			usage()
		}
		writeJSON(buildVersion())
	case "doctor":
		if len(os.Args) != 2 {
			usage()
		}
		output, healthy := doctor()
		writeJSON(output)
		if !healthy {
			os.Exit(1)
		}
	case "node":
		if err := runNode(os.Args[2:]); err != nil {
			var replacement *processReplacement
			if errors.As(err, &replacement) {
				if replaceErr := replaceProcess(replacement.binary); replaceErr == nil {
					return
				} else {
					err = fmt.Errorf("activate update: %w", replaceErr)
				}
			}
			fmt.Fprintf(os.Stderr, "bria node: %v\n", err)
			os.Exit(1)
		}
	case "runner":
		if err := runRunner(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "bria runner: %v\n", err)
			os.Exit(1)
		}
	case "cluster":
		if err := runCluster(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "bria cluster: %v\n", err)
			os.Exit(1)
		}
	case "provider-hook":
		if err := runProviderHook(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "bria provider-hook: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}

type processReplacement struct{ binary string }

func (r *processReplacement) Error() string { return "restart into staged Bria release" }

func usage() {
	fmt.Fprintln(os.Stderr, "usage: bria <version|doctor|cluster|node|runner|provider-hook>")
	os.Exit(2)
}

func buildVersion() versionOutput {
	return versionOutput{
		Name: "bria", Version: buildinfo.Version, Commit: buildinfo.Commit,
		Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
	}
}

func doctor() (doctorOutput, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output := doctorOutput{Version: buildVersion()}
	bootID, err := platform.NewBootIDProvider().Current(ctx)
	if err != nil {
		output.BootError = err.Error()
	} else {
		output.BootID = bootID
	}
	runner := runtimehost.ExecCommandRunner{}
	tmux, tmuxErr := runtimehost.NewTmuxProbe(runner, 3*time.Second).Probe(ctx)
	output.Tmux = probeOutput{
		Name: "tmux", Available: tmux.Available, Executable: tmux.Executable,
		Version: tmux.Version, Capabilities: capabilityStrings(tmux.Capabilities),
	}
	if tmuxErr != nil {
		output.Tmux.Error = tmuxErr.Error()
	}
	availableBackends := 0
	for _, probe := range []runtimehost.BackendProbe{
		runtimehost.NewClaudeProbe(runner, 3*time.Second),
		runtimehost.NewCodexProbe(runner, 3*time.Second),
	} {
		descriptor, probeErr := probe.Probe(ctx)
		item := probeOutput{
			Name: descriptor.Name, Available: descriptor.Available,
			Executable: descriptor.Executable, Version: descriptor.Version,
			Capabilities: capabilityStrings(descriptor.Capabilities),
		}
		if probeErr != nil {
			item.Error = probeErr.Error()
		}
		if item.Available {
			availableBackends++
		}
		output.Backends = append(output.Backends, item)
	}
	return output, output.BootID != "" && output.Tmux.Available && availableBackends > 0
}

func capabilityStrings(capabilities []runtimehost.Capability) []string {
	result := make([]string, len(capabilities))
	for index, capability := range capabilities {
		result[index] = string(capability)
	}
	return result
}

func writeJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "encode output: %v\n", err)
		os.Exit(1)
	}
}
