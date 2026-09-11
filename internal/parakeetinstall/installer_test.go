package parakeetinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCreatesVerifiedRuntimeModelAndWrapperAndIsIdempotent(t *testing.T) {
	runtime := runtimeArchive(t, "nemo-test", map[string]archiveEntry{
		"bin/nemo-speech":  {mode: 0o755, content: "nemo"},
		"lib/libnemo.so.1": {mode: 0o644, content: "library"},
		"lib/libnemo.so":   {mode: tar.TypeSymlink, content: "libnemo.so.1"},
	})
	model := []byte("verified-model")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		switch request.URL.Path {
		case "/runtime":
			_, _ = response.Write(runtime)
		case "/model":
			_, _ = response.Write(model)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	root := canonicalInstallerTempDir(t)
	ffmpeg := filepath.Join(root, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("ffmpeg"), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := Paths{
		Executable: filepath.Join(root, "tools", "bria-parakeet"),
		Model:      filepath.Join(root, "models", "parakeet.gguf"),
	}
	plan := testPlan(server.URL, runtime, model)
	deps := testDependencies(ffmpeg)
	if err := install(context.Background(), paths, plan, deps); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("download requests = %d, want 2", requests)
	}
	assertContent(t, paths.Model, model)
	assertContent(t, filepath.Join(root, "tools", "nemo-speech", "bin", "nemo-speech"), []byte("nemo"))
	link, err := os.Readlink(filepath.Join(root, "tools", "nemo-speech", "lib", "libnemo.so"))
	if err != nil || link != "libnemo.so.1" {
		t.Fatalf("runtime symlink = %q, %v", link, err)
	}
	wrapper, err := os.ReadFile(paths.Executable)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{ffmpeg, filepath.Join(root, "tools", "nemo-speech", "bin", "nemo-speech"), "--quiet", "transcribe"} {
		if !strings.Contains(string(wrapper), required) {
			t.Fatalf("wrapper does not contain %q: %s", required, wrapper)
		}
	}

	server.Close()
	if err := install(context.Background(), paths, plan, deps); err != nil {
		t.Fatalf("idempotent offline install failed: %v", err)
	}
	if err := verify(context.Background(), paths, plan, deps); err != nil {
		t.Fatalf("offline verification failed: %v", err)
	}
}

func TestInstallRejectsDigestMismatchWithoutActivatingArtifacts(t *testing.T) {
	runtime := runtimeArchive(t, "nemo-test", map[string]archiveEntry{
		"bin/nemo-speech": {mode: 0o755, content: "nemo"},
	})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(runtime)
	}))
	defer server.Close()

	root := canonicalInstallerTempDir(t)
	ffmpeg := filepath.Join(root, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("ffmpeg"), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Executable: filepath.Join(root, "tools", "wrapper"), Model: filepath.Join(root, "models", "model")}
	plan := testPlan(server.URL, runtime, []byte("model"))
	plan.Runtime.SHA256 = strings.Repeat("0", 64)
	if err := install(context.Background(), paths, plan, testDependencies(ffmpeg)); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	for _, target := range []string{paths.Executable, paths.Model, filepath.Join(root, "tools", "nemo-speech")} {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("failed install activated %s: %v", target, err)
		}
	}
}

func TestInstallRejectsArchiveEscape(t *testing.T) {
	runtime := runtimeArchive(t, "nemo-test", map[string]archiveEntry{
		"../escape": {mode: 0o644, content: "escaped"},
	})
	model := []byte("model")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/runtime" {
			_, _ = response.Write(runtime)
			return
		}
		_, _ = response.Write(model)
	}))
	defer server.Close()
	root := canonicalInstallerTempDir(t)
	ffmpeg := filepath.Join(root, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("ffmpeg"), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Executable: filepath.Join(root, "tools", "wrapper"), Model: filepath.Join(root, "models", "model")}
	if err := install(context.Background(), paths, testPlan(server.URL, runtime, model), testDependencies(ffmpeg)); err == nil {
		t.Fatal("archive escape was accepted")
	}
	if _, err := os.Lstat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped staging root: %v", err)
	}
}

func TestOfficialPlanPinsEverySupportedNativePlatform(t *testing.T) {
	for _, target := range []struct{ platform, arch string }{
		{"linux", "arm64"}, {"linux", "amd64"}, {"wsl", "arm64"}, {"wsl", "amd64"},
		{"macos", "arm64"}, {"macos", "amd64"},
	} {
		plan, err := officialPlan(target.platform, target.arch)
		if err != nil {
			t.Fatalf("officialPlan(%q, %q): %v", target.platform, target.arch, err)
		}
		for _, artifact := range []artifact{plan.Runtime, plan.Model} {
			if !strings.HasPrefix(artifact.URL, "https://") || artifact.Size <= 0 || len(artifact.SHA256) != 64 {
				t.Fatalf("unbounded artifact for %s/%s: %#v", target.platform, target.arch, artifact)
			}
		}
	}
	if _, err := officialPlan("windows", "amd64"); err == nil {
		t.Fatal("unsupported native Windows plan was accepted")
	}
}

func TestEnsureFFmpegUsesSupportedPackageManagerThenRechecks(t *testing.T) {
	installed := false
	var calls []string
	deps := dependencies{
		Platform: "linux", EUID: func() int { return 0 },
		LookPath: func(name string) (string, error) {
			switch name {
			case "apt-get":
				return "/usr/bin/apt-get", nil
			case "ffmpeg":
				if installed {
					return "/usr/bin/ffmpeg", nil
				}
			}
			return "", os.ErrNotExist
		},
		Run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(arguments, " "))
			if strings.Join(arguments, " ") == "install -y ffmpeg" {
				installed = true
			}
			return nil, nil
		},
	}
	// The returned path is validated against the real fixture, so point the
	// final lookup at a temporary executable rather than the host ffmpeg.
	root := canonicalInstallerTempDir(t)
	fixture := filepath.Join(root, "ffmpeg")
	if err := os.WriteFile(fixture, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	priorLookup := deps.LookPath
	deps.LookPath = func(name string) (string, error) {
		candidate, err := priorLookup(name)
		if name == "ffmpeg" && err == nil {
			return fixture, nil
		}
		return candidate, err
	}
	if got, err := ensureFFmpeg(context.Background(), deps); err != nil || got != fixture {
		t.Fatalf("ensureFFmpeg = %q, %v", got, err)
	}
	want := []string{"apt-get update", "apt-get install -y ffmpeg"}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("package manager calls = %v, want %v", calls, want)
	}
}

func TestInstallRejectsSymlinkTargets(t *testing.T) {
	root := canonicalInstallerTempDir(t)
	realRuntime := filepath.Join(root, "real-runtime")
	if err := os.Mkdir(realRuntime, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeTarget := filepath.Join(root, "tools", "nemo-speech")
	if err := os.Mkdir(filepath.Dir(runtimeTarget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRuntime, runtimeTarget); err != nil {
		t.Fatal(err)
	}
	if err := activateDirectory(filepath.Join(root, "source"), runtimeTarget); err == nil {
		t.Fatal("symlink runtime target was accepted")
	}
}

type archiveEntry struct {
	mode    int64
	content string
}

func canonicalInstallerTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runtimeArchive(t *testing.T, root string, entries map[string]archiveEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, entry := range entries {
		header := &tar.Header{Name: root + "/" + name, Mode: entry.mode}
		if entry.mode == tar.TypeSymlink {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.content
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.content))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testPlan(base string, runtime, model []byte) plan {
	runtimeDigest := fmt.Sprintf("%x", sha256.Sum256(runtime))
	modelDigest := fmt.Sprintf("%x", sha256.Sum256(model))
	return plan{
		Identity: "test-plan",
		Runtime:  artifact{URL: base + "/runtime", Size: int64(len(runtime)), SHA256: runtimeDigest, Root: "nemo-test"},
		Model:    artifact{URL: base + "/model", Size: int64(len(model)), SHA256: modelDigest},
	}
}

func testDependencies(ffmpeg string) dependencies {
	return dependencies{
		Client: &http.Client{}, AllowHTTP: true,
		LookPath: func(name string) (string, error) {
			if name == "ffmpeg" {
				return ffmpeg, nil
			}
			return "", os.ErrNotExist
		},
		Run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			if strings.HasSuffix(name, "nemo-speech") && len(arguments) == 1 && arguments[0] == "--version" {
				return []byte("nemo-speech 0.1.0\n"), nil
			}
			return nil, fmt.Errorf("unexpected command %s %v", name, arguments)
		},
		EUID: func() int { return 0 }, Platform: "linux", Arch: "arm64",
	}
}

func assertContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
