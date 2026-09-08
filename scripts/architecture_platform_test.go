package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestArchitectureChecksEverySupportedPlatform(t *testing.T) {
	for _, target := range []struct{ os, arch string }{
		{"linux", "amd64"}, {"linux", "arm64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
	} {
		t.Run(target.os+"/"+target.arch, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "go.mod"), "module bria\n\ngo 1.22\n")
			writeFile(t, filepath.Join(root, "internal/nativecapture/base.go"), "package nativecapture\n")
			filename := "extra_" + target.os + "_" + target.arch + ".go"
			writeFile(t, filepath.Join(root, "internal/nativecapture", filename),
				"package nativecapture\n"+strings.Repeat("// platform-specific responsibility\n", 61))
			assertErrorContains(t, checkArchitecture(root), target.os+"/"+target.arch+
				": production size exceeds registered responsibility budget: internal/nativecapture")
		})
	}
}

func TestArchitectureChecksPlatformSpecificImports(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module bria\n\ngo 1.22\n")
	writeFile(t, filepath.Join(root, "internal/nativecapture/base.go"), "package nativecapture\n")
	writeFile(t, filepath.Join(root, "internal/domain/base.go"), "package domain\n")
	writeFile(t, filepath.Join(root, "internal/nativecapture/import_linux_arm64.go"),
		"package nativecapture\nimport _ \"bria/internal/domain\"\n")
	assertErrorContains(t, checkArchitecture(root),
		"linux/arm64: package imports dependency outside registered boundary: internal/nativecapture -> internal/domain")
}
