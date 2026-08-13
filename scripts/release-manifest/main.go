package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/clusterupdate"
)

func main() {
	version := flag.String("version", "", "release version")
	directory := flag.String("directory", "", "artifact directory")
	baseURL := flag.String("base-url", "", "HTTPS release asset base URL")
	privateKeyPath := flag.String("private-key", "", "0600 base64 Ed25519 private key")
	output := flag.String("output", "", "manifest output path")
	flag.Parse()
	if flag.NArg() != 0 || *version == "" || *directory == "" || *baseURL == "" ||
		*privateKeyPath == "" || *output == "" {
		fatal(errors.New("all release manifest flags are required"))
	}
	key, err := readPrivateKey(*privateKeyPath)
	if err != nil {
		fatal(err)
	}
	artifacts, err := collectArtifacts(*directory, *version, *baseURL)
	if err != nil {
		fatal(err)
	}
	publishedAt := time.Now().UTC()
	if epoch := os.Getenv("SOURCE_DATE_EPOCH"); epoch != "" {
		seconds, parseErr := strconv.ParseInt(epoch, 10, 64)
		if parseErr != nil {
			fatal(errors.New("SOURCE_DATE_EPOCH is invalid"))
		}
		publishedAt = time.Unix(seconds, 0).UTC()
	}
	manifest, err := clusterupdate.SignManifest(clusterupdate.Manifest{
		Version: *version, PublishedAt: publishedAt, Artifacts: artifacts,
	}, key)
	if err != nil {
		fatal(err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(*output, data, 0o644); err != nil {
		fatal(err)
	}
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("release private key must be a small 0600 regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("release private key must be base64 Ed25519 private key")
	}
	return ed25519.PrivateKey(decoded), nil
}

func collectArtifacts(directory, version, baseURL string) ([]clusterupdate.Artifact, error) {
	pattern := filepath.Join(directory, "bria_"+version+"_*_*.tar.gz")
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) == 0 {
		return nil, errors.New("release artifacts are unavailable")
	}
	sort.Strings(paths)
	artifacts := make([]clusterupdate.Artifact, 0, len(paths)+1)
	prefix := "bria_" + version + "_"
	for _, path := range paths {
		name := filepath.Base(path)
		platform := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".tar.gz")
		parts := strings.Split(platform, "_")
		if len(parts) != 2 {
			return nil, fmt.Errorf("cannot parse artifact platform: %s", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		digest := sha256Sum(data)
		artifact := clusterupdate.Artifact{
			OS: parts[0], Arch: parts[1], URL: strings.TrimRight(baseURL, "/") + "/" + name,
			SHA256: digest, Size: int64(len(data)),
		}
		artifacts = append(artifacts, artifact)
		// Older coordinators compare the node's advertised Android identity
		// literally. Keep this signed alias so they can bootstrap to a version
		// that understands Android's Linux userspace directly.
		if artifact.OS == "linux" && artifact.Arch == "arm64" {
			android := artifact
			android.OS = "android"
			artifacts = append(artifacts, android)
		}
	}
	return artifacts, nil
}

func sha256Sum(data []byte) string {
	hash := sha256.New()
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "release-manifest:", err)
	os.Exit(1)
}
