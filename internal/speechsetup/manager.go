package speechsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/systemdeps"
)

type Config struct {
	NodeID         string
	OS             string
	Arch           string
	DataDir        string
	FFmpegCommand  string
	WhisperCommand string
	WhisperModel   string
	AppleCommand   string
	Dependencies   systemdeps.Config
	Runner         Runner
}

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, command, args...).CombinedOutput()
}

type Manager struct {
	config Config
	runner Runner

	mu     sync.Mutex
	status Status
}

func NewManager(config Config) (*Manager, error) {
	if strings.TrimSpace(config.NodeID) == "" || strings.TrimSpace(config.DataDir) == "" {
		return nil, errors.New("speech setup node and data directory are required")
	}
	if config.Runner == nil {
		config.Runner = execRunner{}
	}
	manager := &Manager{config: config, runner: config.Runner}
	manager.status = manager.initialStatus()
	return manager, nil
}

func (m *Manager) Status(_ context.Context, request Request) (Status, error) {
	if request.NodeID != m.config.NodeID {
		return Status{}, errors.New("speech setup request targets another node")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Phase == PhaseMissing || m.status.Phase == PhaseReady {
		m.status = m.inspect()
	}
	return m.status, nil
}

func (m *Manager) Start(_ context.Context, request Request) (Status, error) {
	if request.NodeID != m.config.NodeID {
		return Status{}, errors.New("speech setup request targets another node")
	}
	m.mu.Lock()
	if m.status.Phase == PhaseInstalling || m.status.Phase == PhaseReady {
		status := m.status
		m.mu.Unlock()
		return status, nil
	}
	m.status = Status{
		NodeID: m.config.NodeID, Engine: m.engine(), Phase: PhaseInstalling,
		Detail: "preparing local speech recognition", UpdatedAt: time.Now(),
	}
	m.persistStatus(m.status)
	status := m.status
	m.mu.Unlock()
	go m.install()
	return status, nil
}

func (m *Manager) install() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	var err error
	phase := PhaseReady
	if m.engine() == "apple" {
		phase, err = m.installApple(ctx)
	} else {
		err = m.installWhisper(ctx)
	}
	status := m.inspect()
	if err != nil {
		status.Phase = phase
		if phase == PhaseReady {
			status.Phase = PhaseFailed
		}
		status.Detail = compactError(err)
		status.UpdatedAt = time.Now()
	} else if m.engine() == "apple" {
		status.Phase, status.Detail = PhaseReady, "Apple Speech is ready"
	}
	m.mu.Lock()
	m.status = status
	m.persistStatus(status)
	m.mu.Unlock()
}

func (m *Manager) installWhisper(ctx context.Context) error {
	if _, err := executable(m.config.FFmpegCommand); err != nil {
		if installErr := m.installSystemDependencies(ctx); installErr != nil {
			return fmt.Errorf("install ffmpeg: %w", installErr)
		}
		if _, retryErr := executable(m.config.FFmpegCommand); retryErr != nil {
			return fmt.Errorf("ffmpeg remains unavailable after installation: %w", retryErr)
		}
	}
	if _, err := executable(m.whisperPath()); err != nil {
		if err := m.buildWhisper(ctx); err != nil {
			return err
		}
	}
	if regularFile(m.config.WhisperModel) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.config.WhisperModel), 0o700); err != nil {
		return fmt.Errorf("create speech model directory: %w", err)
	}
	temporary := m.config.WhisperModel + ".partial"
	url := "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/" +
		filepath.Base(m.config.WhisperModel)
	if _, err := m.runner.Run(ctx, "curl", "--fail", "--location", "--retry", "3",
		"--continue-at", "-",
		"--output", temporary, url); err != nil {
		return fmt.Errorf("download Whisper model: %w", err)
	}
	if err := os.Rename(temporary, m.config.WhisperModel); err != nil {
		return fmt.Errorf("activate Whisper model: %w", err)
	}
	return nil
}

func (m *Manager) buildWhisper(ctx context.Context) error {
	for _, command := range []string{"git", "cmake"} {
		if _, err := exec.LookPath(command); err != nil {
			return fmt.Errorf("%s is required to install Whisper", command)
		}
	}
	root := filepath.Join(m.config.DataDir, "speech", "whisper.cpp")
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return err
	}
	if !regularDir(filepath.Join(root, ".git")) {
		_ = os.RemoveAll(root)
		if output, err := m.runner.Run(ctx, "git", "clone", "--depth", "1",
			"https://github.com/ggerganov/whisper.cpp.git", root); err != nil {
			return commandError("clone whisper.cpp", output, err)
		}
	}
	build := filepath.Join(root, "build")
	if output, err := m.runner.Run(ctx, "cmake", "-S", root, "-B", build,
		"-DCMAKE_BUILD_TYPE=Release", "-DBUILD_SHARED_LIBS=OFF"); err != nil {
		return commandError("configure whisper.cpp", output, err)
	}
	if output, err := m.runner.Run(ctx, "cmake", "--build", build, "--config", "Release", "-j", "2"); err != nil {
		return commandError("build whisper.cpp", output, err)
	}
	source := filepath.Join(build, "bin", "whisper-cli")
	target := m.whisperPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	input, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read built whisper-cli: %w", err)
	}
	if err := os.WriteFile(target, input, 0o700); err != nil {
		return fmt.Errorf("install whisper-cli: %w", err)
	}
	return nil
}

func (m *Manager) installApple(ctx context.Context) (Phase, error) {
	command, err := executable(m.applePath())
	if err != nil {
		return PhaseFailed, errors.New("Apple Speech helper is not installed; deploy the macOS Bria helper first")
	}
	output, err := m.runner.Run(ctx, command, "--authorize")
	if err != nil {
		return PhasePermissionRequired, commandError("authorize Apple Speech", output, err)
	}
	return PhaseReady, nil
}

func (m *Manager) inspect() Status {
	status := Status{NodeID: m.config.NodeID, Engine: m.engine(), UpdatedAt: time.Now()}
	if status.Engine == "apple" {
		if _, err := executable(m.applePath()); err != nil {
			status.Phase, status.Detail = PhaseMissing, "Apple Speech helper is not installed"
			return status
		}
		status.Phase, status.Detail = PhaseMissing, "Apple Speech permission has not been checked"
		return status
	}
	if _, err := executable(m.config.FFmpegCommand); err != nil {
		status.Phase, status.Detail = PhaseMissing, "ffmpeg is not installed"
		return status
	}
	if _, err := executable(m.whisperPath()); err != nil {
		status.Phase, status.Detail = PhaseMissing, "whisper-cli is not installed"
		return status
	}
	if !regularFile(m.config.WhisperModel) {
		status.Phase, status.Detail = PhaseMissing, "Whisper model is not installed"
		return status
	}
	status.Phase, status.Detail = PhaseReady, "Whisper is ready"
	return status
}

func (m *Manager) engine() string {
	if strings.EqualFold(m.config.OS, "darwin") {
		return "apple"
	}
	return "whisper"
}

func (m *Manager) whisperPath() string {
	if path, err := executable(m.config.WhisperCommand); err == nil {
		return path
	}
	return filepath.Join(m.config.DataDir, "speech", "bin", executableName("whisper-cli", m.config.OS))
}

func (m *Manager) applePath() string {
	if path, err := executable(m.config.AppleCommand); err == nil {
		return path
	}
	return filepath.Join(m.config.DataDir, "speech", "bin", "bria-apple-speech")
}

func executable(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("empty executable")
	}
	if strings.ContainsRune(command, filepath.Separator) {
		info, err := os.Stat(command)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			return "", errors.New("executable is unavailable")
		}
		return command, nil
	}
	return exec.LookPath(command)
}

func executableName(name, goos string) string {
	if strings.EqualFold(goos, "windows") || (goos == "" && runtime.GOOS == "windows") {
		return name + ".exe"
	}
	return name
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func regularDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func commandError(stage string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if len(detail) > 400 {
		detail = detail[len(detail)-400:]
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", stage, err)
	}
	return fmt.Errorf("%s: %s", stage, detail)
}

func compactError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 500 {
		return text[:500] + "…"
	}
	return text
}
