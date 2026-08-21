package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Time4Mind/bria/internal/binaryidentity"
)

const maxBriaExecutableBytes int64 = binaryidentity.MaxExecutableBytes

const expectedBinarySHA256Env = "BRIA_EXPECTED_BINARY_SHA256"

func currentBinarySHA256() (string, error) {
	return binaryidentity.Current()
}

func binarySHA256(path string) (string, error) {
	return binaryidentity.SHA256(path)
}

func verifyRunningReleaseIdentity(build versionOutput) error {
	expected := strings.TrimSpace(os.Getenv(expectedBinarySHA256Env))
	if expected != "" && expected != build.BinarySHA256 {
		return errors.New("running Bria binary does not match its launch identity")
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("resolve running Bria release")
	}
	if filepath.Base(filepath.Dir(executable)) == "current" && expected == "" {
		return errors.New("mutable Bria activation requires a launch identity")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return errors.New("resolve running Bria release")
	}
	return verifyReleaseIdentity(resolved, build)
}

func verifyReleaseIdentity(executable string, build versionOutput) error {
	release := filepath.Dir(executable)
	if filepath.Base(filepath.Dir(release)) != "releases" {
		return nil
	}
	metadata, err := os.ReadFile(filepath.Join(release, "release.json"))
	if err != nil || len(metadata) > 8<<10 {
		return errors.New("running Bria release provenance is unavailable")
	}
	var recorded struct {
		Version      string `json:"version"`
		Commit       string `json:"commit"`
		BuiltAt      string `json:"built_at"`
		BinarySHA256 string `json:"binary_sha256"`
	}
	if json.Unmarshal(metadata, &recorded) != nil || recorded.Version != build.Version ||
		recorded.Commit != build.Commit || recorded.BuiltAt != build.BuiltAt ||
		recorded.BinarySHA256 != build.BinarySHA256 {
		return errors.New("running Bria release provenance does not match executable")
	}
	return nil
}

func validBuildVersion(value string) bool {
	return validBuildLabel(value, 1, 128, false)
}

func validBuildCommit(value string) bool {
	if value == "unknown" {
		return true
	}
	return validBuildLabel(value, 7, 64, true)
}

func validBuildTimestamp(value string) bool {
	if value == "unknown" {
		return true
	}
	if value == "" || len(value) > 20 {
		return false
	}
	for _, char := range value {
		if !unicode.IsDigit(char) || char > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func validBuildLabel(value string, minimum, maximum int, hexOnly bool) bool {
	if len(value) < minimum || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if hexOnly {
			if char < '0' || char > '9' && char < 'a' || char > 'f' {
				return false
			}
			continue
		}
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || strings.ContainsRune("._+-", char) {
			continue
		}
		return false
	}
	return true
}
