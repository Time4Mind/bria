package domain

import (
	"fmt"
	"sort"
	"strings"
)

func normalizeBackendDescriptors(backends []BackendDescriptor) ([]BackendDescriptor, error) {
	normalized := cloneBackendDescriptors(backends)
	seen := make(map[string]bool, len(normalized))
	for index, backend := range normalized {
		name := strings.ToLower(strings.TrimSpace(backend.Name))
		if name == "" || seen[name] {
			return nil, fmt.Errorf("backend names must be non-empty and unique")
		}
		seen[name] = true
		normalized[index].Name = name
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Name < normalized[j].Name })
	return normalized, nil
}

func cloneBackendDescriptors(backends []BackendDescriptor) []BackendDescriptor {
	result := append([]BackendDescriptor(nil), backends...)
	for index := range result {
		result[index].Capabilities = append([]string(nil), result[index].Capabilities...)
	}
	return result
}
