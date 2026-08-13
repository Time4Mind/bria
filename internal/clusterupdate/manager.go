package clusterupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhaseDownloading Phase = "downloading"
	PhaseStaged      Phase = "staged"
	PhaseFailed      Phase = "failed"
)

type Request struct {
	NodeID         string `json:"node_id"`
	UpdateID       string `json:"update_id"`
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type Status struct {
	NodeID   string `json:"node_id"`
	UpdateID string `json:"update_id,omitempty"`
	Version  string `json:"version,omitempty"`
	Phase    Phase  `json:"phase"`
	Error    string `json:"error,omitempty"`
}

type Service interface {
	Inspect(context.Context) (VerifiedManifest, error)
	Start(context.Context, Request) (Status, error)
	Status(context.Context, Request) (Status, error)
}

type ManagerConfig struct {
	NodeID         string
	InstallRoot    string
	ActivationPath string
	Fetcher        Fetcher
	Client         *http.Client
	Restart        func(string)
	Watchdog       func(Request) error
}

type Manager struct {
	config ManagerConfig
	mu     sync.Mutex
	status Status
}

func NewManager(config ManagerConfig) (*Manager, error) {
	if config.NodeID == "" || config.InstallRoot == "" || !filepath.IsAbs(config.InstallRoot) ||
		config.ActivationPath == "" || !filepath.IsAbs(config.ActivationPath) ||
		config.Fetcher.URL == "" || len(config.Fetcher.PublicKey) == 0 || config.Restart == nil {
		return nil, errors.New("cluster update manager configuration is incomplete")
	}
	config.InstallRoot = filepath.Clean(config.InstallRoot)
	config.ActivationPath = filepath.Clean(config.ActivationPath)
	if config.Client != nil {
		config.Fetcher.Client = config.Client
	}
	manager := &Manager{config: config, status: Status{NodeID: config.NodeID, Phase: PhaseIdle}}
	manager.loadFailure()
	return manager, nil
}

func (m *Manager) Inspect(ctx context.Context) (VerifiedManifest, error) {
	return m.config.Fetcher.Fetch(ctx)
}

func (m *Manager) Start(ctx context.Context, request Request) (Status, error) {
	if err := m.validateRequest(request); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Phase == PhaseDownloading || m.status.Phase == PhaseStaged {
		if m.status.UpdateID == request.UpdateID {
			return m.status, nil
		}
		return Status{}, errors.New("another node update is already active")
	}
	m.status = Status{
		NodeID: m.config.NodeID, UpdateID: request.UpdateID,
		Version: request.Version, Phase: PhaseDownloading,
	}
	_ = os.Remove(filepath.Join(m.config.InstallRoot, "update-status.json"))
	go m.install(request)
	return m.status, nil
}

func (m *Manager) Status(_ context.Context, request Request) (Status, error) {
	if request.NodeID != m.config.NodeID {
		return Status{}, errors.New("update request targets another node")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status, nil
}

func (m *Manager) validateRequest(request Request) error {
	if request.NodeID != m.config.NodeID || strings.TrimSpace(request.UpdateID) == "" ||
		strings.TrimSpace(request.Version) == "" || len(request.ManifestSHA256) != 64 {
		return errors.New("invalid node update request")
	}
	if _, err := hex.DecodeString(request.ManifestSHA256); err != nil {
		return errors.New("invalid manifest digest")
	}
	return nil
}

func (m *Manager) install(request Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	manifest, err := m.Inspect(ctx)
	if err == nil && !strings.EqualFold(manifest.SHA256, request.ManifestSHA256) {
		err = errors.New("manifest changed after cluster update began")
	}
	if err == nil && manifest.Version != request.Version {
		err = errors.New("manifest version does not match cluster update")
	}
	var artifact Artifact
	if err == nil {
		var ok bool
		artifact, ok = manifest.Artifact(runtime.GOOS, runtime.GOARCH)
		if !ok {
			err = fmt.Errorf("release has no artifact for %s/%s", runtime.GOOS, runtime.GOARCH)
		}
	}
	if err == nil {
		err = m.downloadAndActivate(ctx, request, artifact)
	}
	if err == nil {
		if m.config.Watchdog != nil {
			err = m.config.Watchdog(request)
		} else {
			err = m.startWatchdog(request)
		}
		if err != nil {
			_, _ = restorePending(filepath.Join(m.config.InstallRoot, "update-pending.json"), request.UpdateID)
		}
	}
	m.mu.Lock()
	if err != nil {
		m.status.Phase, m.status.Error = PhaseFailed, shortError(err)
		m.mu.Unlock()
		return
	}
	m.status.Phase = PhaseStaged
	m.mu.Unlock()
	m.config.Restart(m.config.ActivationPath)
}

func (m *Manager) startWatchdog(request Request) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve updater executable: %w", err)
	}
	command := exec.Command(
		executable, "node", "update-watchdog",
		"--install-root", m.config.InstallRoot,
		"--update-id", request.UpdateID,
		"--pid", fmt.Sprint(os.Getpid()),
		"--timeout", "90s",
	)
	command.Stdin = nil
	command.Stdout, command.Stderr = os.Stderr, os.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start update rollback watchdog: %w", err)
	}
	return command.Process.Release()
}

type pendingUpdate struct {
	NodeID      string `json:"node_id"`
	UpdateID    string `json:"update_id"`
	Version     string `json:"version"`
	Previous    string `json:"previous"`
	CurrentLink string `json:"current_link"`
}

func (m *Manager) downloadAndActivate(ctx context.Context, request Request, artifact Artifact) error {
	if err := os.MkdirAll(filepath.Join(m.config.InstallRoot, "releases"), 0o700); err != nil {
		return fmt.Errorf("create update release root: %w", err)
	}
	temporary, err := os.CreateTemp(m.config.InstallRoot, ".download-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create update download: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := m.download(ctx, temporary, artifact); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	releaseName := safeReleaseName(
		request.Version + "-" + request.UpdateID + "-" + runtime.GOOS + "-" + runtime.GOARCH,
	)
	destination := filepath.Join(m.config.InstallRoot, "releases", releaseName)
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := extractRelease(temporaryPath, destination); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := verifyReleaseBinary(destination, request.Version); err != nil {
		return err
	}
	return m.switchCurrent(request, destination)
}

func (m *Manager) download(ctx context.Context, destination *os.File, artifact Artifact) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	client := m.config.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download update artifact: %w", err)
	}
	defer response.Body.Close()
	if response.Request.URL.Scheme != "https" {
		return errors.New("update artifact redirected outside HTTPS")
	}
	if response.StatusCode != http.StatusOK || response.ContentLength > artifact.Size {
		return fmt.Errorf("download update artifact: HTTP %d", response.StatusCode)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(response.Body, artifact.Size+1))
	if err != nil || written != artifact.Size || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return errors.New("update artifact size or digest mismatch")
	}
	return destination.Sync()
}

func (m *Manager) loadFailure() {
	data, err := os.ReadFile(filepath.Join(m.config.InstallRoot, "update-status.json"))
	if err != nil {
		return
	}
	var status Status
	if json.Unmarshal(data, &status) == nil && status.Phase == PhaseFailed {
		status.NodeID = m.config.NodeID
		m.status = status
	}
}

func shortError(err error) string {
	value := strings.Join(strings.Fields(err.Error()), " ")
	if len(value) > 240 {
		return value[:240]
	}
	return value
}

func safeReleaseName(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || strings.ContainsRune("._-", char) {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-.")
}

func writePending(path string, value pendingUpdate) error {
	temporary := path + ".tmp"
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
