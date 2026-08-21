package clusterupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Time4Mind/bria/internal/binaryidentity"
)

type installedReleaseMetadata struct {
	Schema         int    `json:"schema"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	BuiltAt        string `json:"built_at"`
	BinarySHA256   string `json:"binary_sha256"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	NodeProtocol   int    `json:"node_protocol"`
}

func verifyReleaseBinary(
	destination, version string,
	minimumNodeProtocol int,
	artifactSHA256 string,
) (string, error) {
	binary := releaseBinary(destination)
	output, err := exec.Command(binary, "version").Output()
	if err != nil {
		return "", fmt.Errorf("verify staged Bria binary: %w", err)
	}
	var value struct {
		Version      string `json:"version"`
		Commit       string `json:"commit"`
		BuiltAt      string `json:"built_at"`
		BinarySHA256 string `json:"binary_sha256"`
		NodeProtocol int    `json:"node_protocol"`
	}
	if json.Unmarshal(output, &value) != nil || value.Version != version {
		return "", errors.New("staged Bria version does not match manifest")
	}
	if minimumNodeProtocol > 0 && value.NodeProtocol < minimumNodeProtocol {
		return "", errors.New("staged Bria binary does not satisfy manifest node protocol")
	}
	if !exactReleaseCommit(value.Commit) || !exactReleaseTimestamp(value.BuiltAt) {
		return "", errors.New("staged Bria binary provenance is incomplete")
	}
	actual, err := binaryidentity.SHA256(binary)
	if err != nil || value.BinarySHA256 == "" || value.BinarySHA256 != actual {
		return "", errors.New("staged Bria binary identity does not match executable")
	}
	metadata := installedReleaseMetadata{
		Schema: 1, Version: value.Version, Commit: value.Commit, BuiltAt: value.BuiltAt,
		BinarySHA256: actual, ArtifactSHA256: artifactSHA256, NodeProtocol: value.NodeProtocol,
	}
	if err := normalizeRuntimeRelease(destination); err != nil {
		return "", err
	}
	if err := writeInstalledReleaseMetadata(destination, metadata); err != nil {
		return "", err
	}
	if err := freezeInstalledRelease(destination); err != nil {
		return "", err
	}
	return actual, nil
}

func normalizeRuntimeRelease(destination string) error {
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "bria", "bria.exe", "bria-apple-speech", "release.json":
			continue
		}
		if err := os.RemoveAll(filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func exactReleaseCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' && char < 'a' || char > 'f' {
			return false
		}
	}
	return true
}

func exactReleaseTimestamp(value string) bool {
	if value == "" || len(value) > 20 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func freezeInstalledRelease(destination string) error {
	return filepath.WalkDir(destination, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("installed release contains a symlink")
		}
		if entry.IsDir() {
			// Keep directories traversable and removable by bounded retention.
			// Release files themselves are read-only and are never overwritten.
			return os.Chmod(path, 0o755)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("installed release contains a non-regular file")
		}
		mode := os.FileMode(0o444)
		if info.Mode()&0o111 != 0 {
			mode = 0o555
		}
		return os.Chmod(path, mode)
	})
}

func writeInstalledReleaseMetadata(destination string, metadata installedReleaseMetadata) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(destination, "release.json")
	temporary, err := os.CreateTemp(destination, ".release-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o444); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func verifyInstalledReleaseMetadata(destination string) error {
	data, err := os.ReadFile(filepath.Join(destination, "release.json"))
	if err != nil || len(data) > 8<<10 {
		return errors.New("existing release provenance is unavailable")
	}
	var metadata installedReleaseMetadata
	if json.Unmarshal(data, &metadata) != nil || metadata.Schema != 1 ||
		metadata.Version == "" || !exactReleaseCommit(metadata.Commit) ||
		!exactReleaseTimestamp(metadata.BuiltAt) {
		return errors.New("existing release provenance is invalid")
	}
	actual, err := binaryidentity.SHA256(releaseBinary(destination))
	if err != nil || metadata.BinarySHA256 != actual {
		return errors.New("existing release binary does not match provenance")
	}
	return nil
}

func releaseBinary(destination string) string {
	binary := filepath.Join(destination, "bria")
	if _, err := os.Stat(binary); err != nil {
		binary += ".exe"
	}
	return binary
}
