package main

import (
	"context"
	"sort"
	"time"

	"github.com/Time4Mind/bria/internal/buildinfo"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func discoverLocalBackends(
	ctx context.Context,
	runner runtimehost.CommandRunner,
) []domain.BackendDescriptor {
	if descriptor, err := runtimehost.NewTmuxProbe(runner, 3*time.Second).Probe(ctx); err != nil || !descriptor.Available {
		return nil
	}
	probes := []runtimehost.BackendProbe{
		runtimehost.NewClaudeProbe(runner, 3*time.Second),
		runtimehost.NewCodexProbe(runner, 3*time.Second),
	}
	backends := make([]domain.BackendDescriptor, 0, len(probes))
	for _, probe := range probes {
		descriptor, err := probe.Probe(ctx)
		if err != nil || !descriptor.Available {
			continue
		}
		capabilities := make([]string, len(descriptor.Capabilities))
		for index, capability := range descriptor.Capabilities {
			capabilities[index] = string(capability)
		}
		backends = append(backends, domain.BackendDescriptor{
			Name: descriptor.Name, Version: descriptor.Version, Capabilities: capabilities,
		})
	}
	sort.Slice(backends, func(i, j int) bool { return backends[i].Name < backends[j].Name })
	return backends
}

func localBuildVersion() string {
	return buildinfo.Version
}
