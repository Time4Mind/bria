// Package parakeetinstall provisions the exact pinned local speech runtime
// required by a Bria executor.
package parakeetinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	maximumExtractedRuntime = 512 << 20
	maximumArchiveEntries   = 4096
	maximumCommandOutput    = 64 << 10
)

// Paths are the exact executable and model references already present in the
// validated Bria configuration.
type Paths struct {
	Executable string
	Model      string
}

type dependencies struct {
	Client    *http.Client
	LookPath  func(string) (string, error)
	Run       func(context.Context, string, ...string) ([]byte, error)
	EUID      func() int
	Platform  string
	Arch      string
	AllowHTTP bool
}

type receipt struct {
	SchemaVersion int    `json:"schema_version"`
	Identity      string `json:"identity"`
	RuntimeSHA256 string `json:"runtime_sha256"`
	RuntimeSize   int64  `json:"runtime_size"`
	RuntimeRoot   string `json:"runtime_root"`
}

// Install installs or verifies every local dependency used by the configured
// Parakeet wrapper. Network content is activated only after exact size and
// SHA-256 verification.
func Install(ctx context.Context, paths Paths) error {
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "macos"
	}
	selected, err := officialPlan(platform, runtime.GOARCH)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Hour}
	return install(ctx, paths, selected, dependencies{
		Client: client, LookPath: exec.LookPath, Run: runCommand,
		EUID: os.Geteuid, Platform: platform, Arch: runtime.GOARCH,
	})
}

// Verify checks the same pinned native artifact contract without downloading,
// installing packages, or modifying local state.
func Verify(ctx context.Context, paths Paths) error {
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "macos"
	}
	selected, err := officialPlan(platform, runtime.GOARCH)
	if err != nil {
		return err
	}
	return verify(ctx, paths, selected, dependencies{LookPath: exec.LookPath, Run: runCommand})
}

func verify(ctx context.Context, paths Paths, selected plan, deps dependencies) error {
	if ctx == nil || errPath(paths.Executable) != nil || errPath(paths.Model) != nil || paths.Executable == paths.Model || deps.LookPath == nil || deps.Run == nil {
		return errors.New("invalid Parakeet installation configuration")
	}
	ffmpeg, err := deps.LookPath("ffmpeg")
	if err != nil {
		return errors.New("ffmpeg is unavailable")
	}
	ffmpeg, err = exactExecutable(ffmpeg)
	if err != nil {
		return fmt.Errorf("verify ffmpeg: %w", err)
	}
	runtimeRoot := filepath.Join(filepath.Dir(paths.Executable), "nemo-speech")
	want := receipt{SchemaVersion: 1, Identity: selected.Identity, RuntimeSHA256: selected.Runtime.SHA256, RuntimeSize: selected.Runtime.Size, RuntimeRoot: selected.Runtime.Root}
	if !runtimeReady(ctx, runtimeRoot, want, deps.Run) {
		return errors.New("pinned NeMo-Speech.cpp runtime is unavailable or invalid")
	}
	if !exactFile(paths.Model, selected.Model.Size, selected.Model.SHA256) {
		return errors.New("pinned Parakeet model is unavailable or invalid")
	}
	wantWrapper := wrapperScript(ffmpeg, filepath.Join(runtimeRoot, "bin", "nemo-speech"))
	info, err := os.Lstat(paths.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return errors.New("Parakeet wrapper is unavailable or invalid")
	}
	wrapper, err := os.ReadFile(paths.Executable)
	if err != nil || !bytes.Equal(wrapper, wantWrapper) {
		return errors.New("Parakeet wrapper does not match the pinned installation")
	}
	return nil
}

func install(ctx context.Context, paths Paths, selected plan, deps dependencies) error {
	if ctx == nil || errPath(paths.Executable) != nil || errPath(paths.Model) != nil ||
		paths.Executable == paths.Model || deps.Client == nil || deps.LookPath == nil || deps.Run == nil || deps.EUID == nil {
		return errors.New("invalid Parakeet installation configuration")
	}
	if selected.Runtime.Root == "" || errArtifact(selected.Runtime, deps.AllowHTTP) != nil || errArtifact(selected.Model, deps.AllowHTTP) != nil {
		return errors.New("invalid Parakeet artifact plan")
	}
	ffmpeg, err := ensureFFmpeg(ctx, deps)
	if err != nil {
		return err
	}
	runtimeRoot := filepath.Join(filepath.Dir(paths.Executable), "nemo-speech")
	if err := installRuntime(ctx, runtimeRoot, selected, deps); err != nil {
		return fmt.Errorf("install NeMo-Speech.cpp: %w", err)
	}
	if err := installModel(ctx, paths.Model, selected.Model, deps); err != nil {
		return fmt.Errorf("install Parakeet model: %w", err)
	}
	wrapper := wrapperScript(ffmpeg, filepath.Join(runtimeRoot, "bin", "nemo-speech"))
	if err := installExactFile(paths.Executable, wrapper, 0o700); err != nil {
		return fmt.Errorf("install Parakeet wrapper: %w", err)
	}
	return nil
}

func ensureFFmpeg(ctx context.Context, deps dependencies) (string, error) {
	if candidate, err := deps.LookPath("ffmpeg"); err == nil {
		return exactExecutable(candidate)
	}
	type command struct {
		name string
		args []string
	}
	var commands []command
	switch deps.Platform {
	case "macos":
		commands = []command{{name: "brew", args: []string{"install", "ffmpeg"}}}
	case "linux", "wsl":
		for _, option := range []command{
			{name: "apt-get"},
			{name: "apk", args: []string{"add", "--no-cache", "ffmpeg"}},
			{name: "dnf", args: []string{"install", "-y", "ffmpeg"}},
			{name: "pacman", args: []string{"-Sy", "--noconfirm", "--needed", "ffmpeg"}},
		} {
			if _, err := deps.LookPath(option.name); err == nil {
				if option.name == "apt-get" {
					commands = []command{{name: "apt-get", args: []string{"update"}}, {name: "apt-get", args: []string{"install", "-y", "ffmpeg"}}}
				} else {
					commands = []command{option}
				}
				break
			}
		}
	default:
		return "", errUnsupportedPlatform
	}
	if len(commands) == 0 {
		return "", errors.New("ffmpeg is missing and no supported package manager was found")
	}
	for _, selected := range commands {
		name, arguments := selected.name, selected.args
		if deps.EUID() != 0 {
			if _, err := deps.LookPath("sudo"); err != nil {
				return "", errors.New("ffmpeg installation requires root or sudo")
			}
			arguments = append([]string{name}, arguments...)
			name = "sudo"
		}
		if _, err := deps.Run(ctx, name, arguments...); err != nil {
			return "", fmt.Errorf("install ffmpeg with %s: %w", selected.name, err)
		}
	}
	candidate, err := deps.LookPath("ffmpeg")
	if err != nil {
		return "", errors.New("ffmpeg remains unavailable after installation")
	}
	return exactExecutable(candidate)
}

func installRuntime(ctx context.Context, destination string, selected plan, deps dependencies) error {
	want := receipt{SchemaVersion: 1, Identity: selected.Identity, RuntimeSHA256: selected.Runtime.SHA256, RuntimeSize: selected.Runtime.Size, RuntimeRoot: selected.Runtime.Root}
	if runtimeReady(ctx, destination, want, deps.Run) {
		return nil
	}
	parent, err := secureParent(destination)
	if err != nil {
		return err
	}
	archive, err := os.CreateTemp(parent, ".bria-nemo-*.tar.gz")
	if err != nil {
		return err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	if err := download(ctx, deps.Client, selected.Runtime, archive, deps.AllowHTTP); err != nil {
		archive.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".bria-nemo-stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	root, err := extractRuntime(archivePath, staging, selected.Runtime.Root)
	if err != nil {
		return err
	}
	binary := filepath.Join(root, "bin", "nemo-speech")
	if _, err := exactExecutable(binary); err != nil {
		return errors.New("runtime archive does not contain an executable nemo-speech")
	}
	output, err := deps.Run(ctx, binary, "--version")
	if err != nil || string(output) != "nemo-speech "+runtimeVersion+"\n" {
		return errors.New("runtime version probe did not match the pinned release")
	}
	receiptDocument, err := json.Marshal(want)
	if err != nil {
		return err
	}
	receiptDocument = append(receiptDocument, '\n')
	if err := os.WriteFile(filepath.Join(root, ".bria-parakeet-install.json"), receiptDocument, 0o600); err != nil {
		return err
	}
	return activateDirectory(root, destination)
}

func runtimeReady(ctx context.Context, root string, want receipt, run func(context.Context, string, ...string) ([]byte, error)) bool {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	receiptPath := filepath.Join(root, ".bria-parakeet-install.json")
	receiptInfo, err := os.Lstat(receiptPath)
	if err != nil || !receiptInfo.Mode().IsRegular() || receiptInfo.Mode()&os.ModeSymlink != 0 || receiptInfo.Size() > 4096 {
		return false
	}
	document, err := os.ReadFile(receiptPath)
	if err != nil || len(document) > 4096 {
		return false
	}
	var got receipt
	if json.Unmarshal(document, &got) != nil || got != want {
		return false
	}
	binary, err := exactExecutable(filepath.Join(root, "bin", "nemo-speech"))
	if err != nil {
		return false
	}
	output, err := run(ctx, binary, "--version")
	return err == nil && string(output) == "nemo-speech "+runtimeVersion+"\n"
}

func installModel(ctx context.Context, destination string, source artifact, deps dependencies) error {
	if exactFile(destination, source.Size, source.SHA256) {
		return nil
	}
	parent, err := secureParent(destination)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".bria-parakeet-model-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if err := download(ctx, deps.Client, source, temporary, deps.AllowHTTP); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return activateFile(temporaryPath, destination)
}

func download(ctx context.Context, client *http.Client, source artifact, output *os.File, allowHTTP bool) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Request == nil ||
		(response.Request.URL.Scheme != "https" && !(allowHTTP && response.Request.URL.Scheme == "http")) ||
		(response.ContentLength >= 0 && response.ContentLength != source.Size) {
		return errors.New("artifact source returned an invalid response")
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(output, hash), io.LimitReader(response.Body, source.Size+1))
	if err != nil {
		return err
	}
	if written != source.Size || hex.EncodeToString(hash.Sum(nil)) != source.SHA256 {
		return errors.New("artifact size or SHA-256 mismatch")
	}
	return output.Sync()
}

func extractRuntime(archivePath, staging, expectedRoot string) (string, error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return "", err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var count, total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		count++
		if count > maximumArchiveEntries || header.Size < 0 {
			return "", errors.New("runtime archive exceeds extraction limits")
		}
		clean := path.Clean(strings.ReplaceAll(header.Name, "\\", "/"))
		if clean != expectedRoot && !strings.HasPrefix(clean, expectedRoot+"/") {
			return "", errors.New("runtime archive escapes its expected root")
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(clean, expectedRoot), "/")
		target := filepath.Join(staging, expectedRoot, filepath.FromSlash(relative))
		if relative == "" && header.Typeflag != tar.TypeDir {
			return "", errors.New("runtime archive root is not a directory")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg, tar.TypeRegA:
			total += header.Size
			if total > maximumExtractedRuntime {
				return "", errors.New("runtime archive exceeds extraction limits")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return "", err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return "", copyErr
			}
			if closeErr != nil {
				return "", closeErr
			}
		case tar.TypeSymlink:
			link := path.Clean(strings.ReplaceAll(header.Linkname, "\\", "/"))
			resolved := path.Clean(path.Join(path.Dir(relative), link))
			if path.IsAbs(link) || link == ".." || strings.HasPrefix(link, "../") || resolved == ".." || strings.HasPrefix(resolved, "../") {
				return "", errors.New("runtime archive contains an escaping symlink")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			if err := os.Symlink(filepath.FromSlash(link), target); err != nil {
				return "", err
			}
		default:
			return "", errors.New("runtime archive contains an unsupported entry")
		}
	}
	root := filepath.Join(staging, expectedRoot)
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("runtime archive root is missing")
	}
	return root, nil
}

func installExactFile(destination string, content []byte, mode os.FileMode) error {
	if info, err := os.Lstat(destination); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		if existing, readErr := os.ReadFile(destination); readErr == nil && bytes.Equal(existing, content) {
			return os.Chmod(destination, mode)
		}
	}
	parent, err := secureParent(destination)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".bria-wrapper-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return activateFile(temporaryPath, destination)
}

func activateDirectory(source, destination string) error {
	backup := destination + ".previous"
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("runtime target is not a real directory")
		}
		if _, backupErr := os.Lstat(backup); !os.IsNotExist(backupErr) {
			return errors.New("previous runtime backup already exists")
		}
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
		if err := os.Rename(source, destination); err != nil {
			_ = os.Rename(backup, destination)
			return err
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(source, destination)
}

func activateFile(source, destination string) error {
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("installation target is not a regular file")
		}
		backup := destination + ".previous"
		if _, backupErr := os.Lstat(backup); !os.IsNotExist(backupErr) {
			return errors.New("previous artifact backup already exists")
		}
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
		if err := os.Rename(source, destination); err != nil {
			_ = os.Rename(backup, destination)
			return err
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(source, destination)
}

func secureParent(destination string) (string, error) {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return "", errors.New("installation parent contains a symbolic link")
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("installation parent is not a directory")
	}
	return parent, nil
}

func exactFile(filename string, size int64, digest string) bool {
	before, err := os.Lstat(filename)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() != size {
		return false
	}
	file, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != size {
		return false
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, size+1)); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == digest
}

func exactExecutable(filename string) (string, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("command is not an executable regular file")
	}
	return resolved, nil
}

func errPath(filename string) error {
	if !filepath.IsAbs(filename) || filepath.Clean(filename) != filename || strings.ContainsRune(filename, 0) || strings.ContainsAny(filename, "\r\n") {
		return errors.New("path must be absolute and clean")
	}
	return nil
}

func errArtifact(source artifact, allowHTTP bool) error {
	parsed, err := url.Parse(source.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" ||
		(parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http")) || source.Size <= 0 || len(source.SHA256) != 64 {
		return errors.New("invalid artifact")
	}
	if _, err := hex.DecodeString(source.SHA256); err != nil {
		return errors.New("invalid artifact digest")
	}
	return nil
}

func wrapperScript(ffmpeg, nemo string) []byte {
	return []byte("#!/bin/sh\nset -eu\n" +
		"test \"$#\" -eq 2 || { printf '%s\\n' 'usage: bria-parakeet MODEL AUDIO' >&2; exit 2; }\n" +
		"wav_file=$(mktemp \"${TMPDIR:-/tmp}/bria-parakeet.XXXXXX.wav\")\n" +
		"cleanup() { unlink \"$wav_file\" 2>/dev/null || true; }\n" +
		"trap cleanup EXIT HUP INT TERM\n" +
		shellQuote(ffmpeg) + " -hide_banner -loglevel error -nostdin -y -i \"$2\" -ac 1 -ar 16000 -c:a pcm_s16le \"$wav_file\"\n" +
		shellQuote(nemo) + " --quiet transcribe \"$wav_file\" --model \"$1\"\n")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func runCommand(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	buffer := &boundedBuffer{remaining: maximumCommandOutput}
	command.Stdout, command.Stderr = buffer, buffer
	err := command.Run()
	if err != nil {
		return buffer.Bytes(), err
	}
	return buffer.Bytes(), nil
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if len(value) > b.remaining {
		value = value[:b.remaining]
	}
	_, _ = b.buffer.Write(value)
	b.remaining -= len(value)
	return original, nil
}

func (b *boundedBuffer) Bytes() []byte { return append([]byte(nil), b.buffer.Bytes()...) }
