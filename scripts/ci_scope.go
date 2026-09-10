package main

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	ciScopeDocsOnly = "docs-only"
	ciScopeFull     = "full"
)

func classifyCIScope(paths []string) string {
	if len(paths) == 0 {
		return ciScopeFull
	}
	for _, candidate := range paths {
		if candidate == "" || strings.Contains(candidate, "\\") ||
			path.Clean(candidate) != candidate || !isReleaseJournal(candidate) {
			return ciScopeFull
		}
	}
	return ciScopeDocsOnly
}

func isReleaseJournal(candidate string) bool {
	if candidate == "docs/STATUS_AND_NEXT.md" {
		return true
	}
	name := strings.TrimPrefix(candidate, "docs/")
	return name != candidate && !strings.Contains(name, "/") &&
		strings.HasSuffix(name, "_TODO.md")
}

func runCIScope(input io.Reader, output io.Writer) error {
	data, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("read changed paths: %w", err)
	}
	if len(data) > 0 && data[len(data)-1] != 0 {
		_, err = fmt.Fprintln(output, ciScopeFull)
		return err
	}
	parts := bytes.Split(data, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		paths = append(paths, string(part))
	}
	_, err = fmt.Fprintln(output, classifyCIScope(paths))
	return err
}
