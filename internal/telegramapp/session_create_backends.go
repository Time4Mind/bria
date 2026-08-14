package telegramapp

import (
	"sort"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func createBackends(node domain.Node) []string {
	if !node.BackendExecutionAllowed() {
		return nil
	}
	return createBackendDescriptors(node.Backends)
}

func createBackendDescriptors(backends []domain.BackendDescriptor) []string {
	result := make([]string, 0, len(backends))
	for _, backend := range backends {
		if backendSupportsCreate(backend) {
			result = append(result, strings.ToLower(backend.Name))
		}
	}
	sort.Strings(result)
	return result
}

func backendSupportsCreate(backend domain.BackendDescriptor) bool {
	for _, capability := range backend.Capabilities {
		if capability == string(runtimehost.CapabilitySessionCreate) {
			return true
		}
	}
	return false
}
