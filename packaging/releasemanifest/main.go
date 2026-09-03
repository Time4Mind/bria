// Command releasemanifest creates and verifies owner-signed canonical Bria
// release manifests. Private key material is read only from the mode-0600
// PKCS#8 PEM file named by RELEASE_SIGNING_KEY_FILE. It is never accepted in
// process arguments or written to release output.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"bria/internal/update"
)

const (
	signedManifestName = "release-manifest.json"
	maxKeyFileBytes    = 64 << 10
)

type trustDocument struct {
	FormatVersion int        `json:"format_version"`
	Keys          []trustKey `json:"keys"`
}

type trustKey struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "releasemanifest: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("sign or verify command is required")
	}
	flags := flag.NewFlagSet("releasemanifest "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	releaseDir := flags.String("release-dir", "", "absolute release directory")
	manifestPath := flags.String("manifest", "", "absolute signed manifest path")
	artifactPath := flags.String("artifact", "", "absolute selected artifact path")
	installedPath := flags.String("installed", "", "absolute installed bundle path")
	version := flags.String("version", "", "expected release version")
	platform := flags.String("platform", "", "expected artifact operating system")
	arch := flags.String("arch", "", "expected artifact architecture")
	trustFile := flags.String("trust-file", "", "absolute trusted public keys JSON file")
	keyID := flags.String("key-id", "", "trusted signing key reference")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return errors.New("invalid command arguments")
	}
	switch args[0] {
	case "sign":
		if !filepath.IsAbs(*releaseDir) || !filepath.IsAbs(*trustFile) || *version == "" ||
			*manifestPath != "" || *artifactPath != "" || *installedPath != "" || *platform != "" || *arch != "" {
			return errors.New("absolute release/trust paths and version are required")
		}
		privateKeyFile := os.Getenv("RELEASE_SIGNING_KEY_FILE")
		if !filepath.IsAbs(privateKeyFile) || *keyID == "" {
			return errors.New("RELEASE_SIGNING_KEY_FILE and key_id are required")
		}
		return signRelease(*releaseDir, *version, *keyID, privateKeyFile, *trustFile)
	case "verify":
		if !filepath.IsAbs(*releaseDir) || !filepath.IsAbs(*trustFile) || *version == "" || *keyID != "" ||
			*manifestPath != "" || *artifactPath != "" || *installedPath != "" || *platform != "" || *arch != "" {
			return errors.New("verify does not accept signing arguments")
		}
		return verifyRelease(*releaseDir, *version, *trustFile)
	case "verify-artifact":
		if *releaseDir != "" || *keyID != "" || *installedPath != "" || !filepath.IsAbs(*manifestPath) || !filepath.IsAbs(*artifactPath) ||
			!filepath.IsAbs(*trustFile) || *version == "" || *platform == "" || *arch == "" {
			return errors.New("absolute manifest/artifact/trust paths and exact target are required")
		}
		return verifySelectedArtifact(*manifestPath, *artifactPath, *version, *platform, *arch, *trustFile)
	case "verify-installed":
		if *releaseDir != "" || *keyID != "" || !filepath.IsAbs(*manifestPath) || !filepath.IsAbs(*artifactPath) ||
			!filepath.IsAbs(*installedPath) || !filepath.IsAbs(*trustFile) || *version == "" || *platform == "" || *arch == "" {
			return errors.New("absolute manifest/artifact/installed/trust paths and exact target are required")
		}
		return verifyInstalledArtifact(*manifestPath, *artifactPath, *installedPath, *version, *platform, *arch, *trustFile)
	default:
		return errors.New("unsupported command")
	}
}

func verifyInstalledArtifact(manifestPath, artifactPath, installedPath, version, platform, arch, trustPath string) error {
	selected, err := selectedArtifactMetadata(manifestPath, artifactPath, version, platform, arch, trustPath)
	if err != nil {
		return err
	}
	installedInfo, err := os.Lstat(installedPath)
	if err != nil || !installedInfo.IsDir() || installedInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("installed bundle is not a managed directory")
	}
	archive, err := openRegular(artifactPath)
	if err != nil {
		return errors.New("open selected release artifact")
	}
	defer archive.Close()
	if err := update.VerifyArtifact(archive, selected); err != nil {
		return err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return errors.New("rewind selected release artifact")
	}
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return errors.New("open selected release archive")
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	root := strings.TrimSuffix(filepath.Base(artifactPath), ".tar.gz")
	seen := make(map[string]struct{})
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return errors.New("read selected release archive")
		}
		name := filepath.ToSlash(header.Name)
		if name == root || name == root+"/" {
			continue
		}
		prefix := root + "/"
		if !strings.HasPrefix(name, prefix) {
			return errors.New("selected release archive root mismatch")
		}
		relative := strings.TrimPrefix(name, prefix)
		if relative == "" || relative != pathCleanSlash(relative) {
			return errors.New("selected release archive path mismatch")
		}
		if _, duplicate := seen[relative]; duplicate {
			return errors.New("selected release archive duplicate member")
		}
		seen[relative] = struct{}{}
		target := filepath.Join(installedPath, filepath.FromSlash(relative))
		actual, statErr := os.Lstat(target)
		if statErr != nil || actual.Mode()&os.ModeSymlink != 0 {
			return errors.New("installed bundle member mismatch")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if !actual.IsDir() {
				return errors.New("installed bundle directory mismatch")
			}
		case tar.TypeReg, tar.TypeRegA:
			if !actual.Mode().IsRegular() || actual.Size() != header.Size {
				return errors.New("installed bundle file metadata mismatch")
			}
			file, openErr := os.Open(target)
			if openErr != nil {
				return errors.New("open installed bundle member")
			}
			archiveHash := sha256.New()
			installedHash := sha256.New()
			_, archiveErr := io.Copy(archiveHash, reader)
			_, installedErr := io.Copy(installedHash, file)
			closeErr := file.Close()
			if archiveErr != nil || installedErr != nil || closeErr != nil || !bytes.Equal(archiveHash.Sum(nil), installedHash.Sum(nil)) {
				return errors.New("installed bundle file digest mismatch")
			}
		default:
			return errors.New("selected release archive contains unsupported member")
		}
	}
	if len(seen) == 0 {
		return errors.New("selected release archive is empty")
	}
	return filepath.WalkDir(installedPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("walk installed bundle")
		}
		if path == installedPath {
			return nil
		}
		relative, relErr := filepath.Rel(installedPath, path)
		if relErr != nil {
			return errors.New("walk installed bundle")
		}
		if _, found := seen[filepath.ToSlash(relative)]; !found {
			return errors.New("installed bundle contains unsigned member")
		}
		return nil
	})
}

func pathCleanSlash(value string) string {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || strings.Contains(value, "\\") {
		return ""
	}
	return cleaned
}

func verifySelectedArtifact(manifestPath, artifactPath, expectedVersion, platform, arch, trustPath string) error {
	selected, err := selectedArtifactMetadata(manifestPath, artifactPath, expectedVersion, platform, arch, trustPath)
	if err != nil {
		return err
	}
	file, err := openRegular(artifactPath)
	if err != nil {
		return errors.New("open selected release artifact")
	}
	verifyErr := update.VerifyArtifact(file, selected)
	closeErr := file.Close()
	if verifyErr != nil {
		return verifyErr
	}
	if closeErr != nil {
		return errors.New("close selected release artifact")
	}
	return nil
}

func selectedArtifactMetadata(manifestPath, artifactPath, expectedVersion, platform, arch, trustPath string) (update.Artifact, error) {
	signed, err := readBoundedRegular(manifestPath, maxKeyFileBytes, false)
	if err != nil {
		return update.Artifact{}, errors.New("read signed release manifest")
	}
	trustedKeys, err := loadTrustedKeys(trustPath)
	if err != nil {
		return update.Artifact{}, err
	}
	manifest, _, err := update.VerifySignedManifest(signed, trustedKeys)
	if err != nil {
		return update.Artifact{}, err
	}
	if manifest.Version != expectedVersion || len(manifest.Artifacts) != 4 {
		return update.Artifact{}, fmt.Errorf("%w: unexpected release identity", update.ErrInvalidManifest)
	}
	for _, target := range releaseTargets(expectedVersion) {
		signedArtifact, selectErr := update.SelectArtifact(manifest, target.platform, target.arch)
		if selectErr != nil || signedArtifact.Name != target.name {
			return update.Artifact{}, fmt.Errorf("%w: unexpected artifact identity", update.ErrInvalidManifest)
		}
	}
	selected, err := update.SelectArtifact(manifest, platform, arch)
	if err != nil || selected.Name != filepath.Base(artifactPath) {
		return update.Artifact{}, fmt.Errorf("%w: selected artifact identity mismatch", update.ErrInvalidManifest)
	}
	return selected, nil
}

func signRelease(releaseDir, version, keyID, privateKeyPath, trustPath string) error {
	sources, closeSources, err := artifactSources(releaseDir, version)
	if err != nil {
		return err
	}
	defer closeSources()
	manifest, err := update.BuildManifest(version, sources)
	if err != nil {
		return err
	}
	privateKey, err := loadPrivateKey(privateKeyPath)
	if err != nil {
		return err
	}
	defer clear(privateKey)
	signed, err := update.SignManifest(manifest, keyID, privateKey)
	if err != nil {
		return err
	}
	trustedKeys, err := loadTrustedKeys(trustPath)
	if err != nil {
		return err
	}
	verified, verifiedKeyID, err := update.VerifySignedManifest(signed, trustedKeys)
	if err != nil {
		return err
	}
	if verifiedKeyID != keyID || !reflect.DeepEqual(verified, manifest) {
		return update.ErrInvalidSignature
	}
	return writeExclusive(filepath.Join(releaseDir, signedManifestName), signed)
}

func verifyRelease(releaseDir, expectedVersion, trustPath string) error {
	signed, err := readBoundedRegular(filepath.Join(releaseDir, signedManifestName), maxKeyFileBytes, false)
	if err != nil {
		return errors.New("read signed release manifest")
	}
	trustedKeys, err := loadTrustedKeys(trustPath)
	if err != nil {
		return err
	}
	manifest, _, err := update.VerifySignedManifest(signed, trustedKeys)
	if err != nil {
		return err
	}
	if manifest.Version != expectedVersion || len(manifest.Artifacts) != 4 {
		return fmt.Errorf("%w: unexpected release identity", update.ErrInvalidManifest)
	}
	for _, target := range releaseTargets(expectedVersion) {
		artifact, err := update.SelectArtifact(manifest, target.platform, target.arch)
		if err != nil {
			return err
		}
		if artifact.Name != target.name {
			return fmt.Errorf("%w: unexpected artifact identity", update.ErrInvalidManifest)
		}
		file, err := openRegular(filepath.Join(releaseDir, target.name))
		if err != nil {
			return errors.New("open release artifact")
		}
		verifyErr := update.VerifyArtifact(file, artifact)
		closeErr := file.Close()
		if verifyErr != nil {
			return verifyErr
		}
		if closeErr != nil {
			return errors.New("close release artifact")
		}
	}
	return nil
}

type releaseTarget struct {
	name, platform, arch string
}

func releaseTargets(version string) []releaseTarget {
	return []releaseTarget{
		{name: "bria_" + version + "_darwin_amd64.tar.gz", platform: "darwin", arch: "amd64"},
		{name: "bria_" + version + "_darwin_arm64.tar.gz", platform: "darwin", arch: "arm64"},
		{name: "bria_" + version + "_linux_amd64.tar.gz", platform: "linux", arch: "amd64"},
		{name: "bria_" + version + "_linux_arm64.tar.gz", platform: "linux", arch: "arm64"},
	}
}

func artifactSources(releaseDir, version string) ([]update.ArtifactSource, func(), error) {
	targets := releaseTargets(version)
	sources := make([]update.ArtifactSource, 0, len(targets))
	files := make([]*os.File, 0, len(targets))
	closeFiles := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	for _, target := range targets {
		file, err := openRegular(filepath.Join(releaseDir, target.name))
		if err != nil {
			closeFiles()
			return nil, func() {}, errors.New("open release artifact")
		}
		files = append(files, file)
		sources = append(sources, update.ArtifactSource{
			Name: target.name, Platform: target.platform, Arch: target.arch, Content: file,
		})
	}
	return sources, closeFiles, nil
}

func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	encoded, err := readBoundedRegular(path, maxKeyFileBytes, true)
	if err != nil {
		return nil, errors.New("read signing key file")
	}
	block, rest := pem.Decode(encoded)
	clear(encoded)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("parse signing key file")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	clear(block.Bytes)
	if err != nil {
		return nil, errors.New("parse signing key file")
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("signing key must be Ed25519 PKCS#8")
	}
	return privateKey, nil
}

func loadTrustedKeys(path string) (update.TrustedKeys, error) {
	encoded, err := readBoundedRegular(path, maxKeyFileBytes, false)
	if err != nil {
		return nil, errors.New("read public trust file")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var document trustDocument
	if err := decoder.Decode(&document); err != nil || decoder.Decode(&struct{}{}) != io.EOF || document.FormatVersion != 1 || len(document.Keys) == 0 {
		return nil, errors.New("invalid public trust file")
	}
	canonical, err := json.Marshal(document)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return nil, errors.New("public trust file must be canonical JSON")
	}
	sort.Slice(document.Keys, func(i, j int) bool { return document.Keys[i].KeyID < document.Keys[j].KeyID })
	trusted := make(update.TrustedKeys, len(document.Keys))
	for index, key := range document.Keys {
		if !validLabel(key.KeyID) || (index > 0 && key.KeyID == document.Keys[index-1].KeyID) {
			return nil, errors.New("invalid public trust file")
		}
		decoded, err := base64.StdEncoding.DecodeString(key.PublicKey)
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != key.PublicKey || len(decoded) != ed25519.PublicKeySize {
			return nil, errors.New("invalid public trust file")
		}
		trusted[key.KeyID] = ed25519.PublicKey(append([]byte(nil), decoded...))
	}
	return trusted, nil
}

func readBoundedRegular(path string, limit int64, requirePrivate bool) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > limit {
		return nil, errors.New("unavailable file")
	}
	if requirePrivate && runtime.GOOS != "windows" && before.Mode().Perm() != 0o600 {
		return nil, errors.New("private file permissions must be 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("unavailable file")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("file changed during open")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(content)) > limit {
		return nil, errors.New("unreadable file")
	}
	return content, nil
}

func openRegular(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("artifact is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open artifact")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		file.Close()
		return nil, errors.New("artifact changed during open")
	}
	return file, nil
}

func writeExclusive(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return errors.New("create signed release manifest")
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		file.Close()
		return errors.New("write signed release manifest")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return errors.New("sync signed release manifest")
	}
	if err := file.Close(); err != nil {
		return errors.New("close signed release manifest")
	}
	complete = true
	return nil
}

func validLabel(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '+' || character == '-' {
			continue
		}
		return false
	}
	return true
}
