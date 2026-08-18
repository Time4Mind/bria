package clusterupdate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	PhaseWaiting     Phase = "waiting"
	PhaseInspecting  Phase = "inspecting"
	PhaseDownloading Phase = "downloading"
	PhaseVerifying   Phase = "verifying"
	PhaseExtracting  Phase = "extracting"
	PhasePreflight   Phase = "preflight"
	PhaseActivating  Phase = "activating"
	PhaseRestarting  Phase = "restarting"
	PhaseStaged      Phase = "staged"
	PhaseHealthy     Phase = "healthy"
	PhaseFailed      Phase = "failed"
)

type Request struct {
	NodeID         string `json:"node_id"`
	UpdateID       string `json:"update_id"`
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type Status struct {
	NodeID     string    `json:"node_id"`
	UpdateID   string    `json:"update_id,omitempty"`
	Version    string    `json:"version,omitempty"`
	Phase      Phase     `json:"phase"`
	Progress   int       `json:"progress,omitempty"`
	BytesDone  int64     `json:"bytes_done,omitempty"`
	BytesTotal int64     `json:"bytes_total,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type CompatibilityResult struct {
	Required        bool
	ManifestVersion string
	ActivationPath  string
}

const bootstrapManifestTimeout = 5 * time.Second

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
	Preflight      func(context.Context, string) error
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
		config.Fetcher.URL == "" || len(config.Fetcher.PublicKey) == 0 || config.Preflight == nil ||
		config.Restart == nil {
		return nil, errors.New("cluster update manager configuration is incomplete")
	}
	config.InstallRoot = filepath.Clean(config.InstallRoot)
	config.ActivationPath = filepath.Clean(config.ActivationPath)
	if config.Client != nil {
		config.Fetcher.Client = config.Client
	}
	manager := &Manager{config: config, status: Status{NodeID: config.NodeID, Phase: PhaseIdle}}
	manager.loadStatus()
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
	if activeManagerPhase(m.status.Phase) {
		if m.status.UpdateID == request.UpdateID {
			return m.status, nil
		}
		return Status{}, errors.New("another node update is already active")
	}
	now := time.Now().UTC()
	m.status = Status{
		NodeID: m.config.NodeID, UpdateID: request.UpdateID,
		Version: request.Version, Phase: PhaseInspecting, Progress: 2,
		StartedAt: now, UpdatedAt: now,
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

// EnsureCompatible runs before Raft and uses only the pinned release manifest.
// A compatible node is never forced to update. Once the signed manifest raises
// the floor, activation must succeed before the caller joins the cluster.
func (m *Manager) EnsureCompatible(
	ctx context.Context,
	localProtocol int,
) (CompatibilityResult, error) {
	checkCtx, cancel := context.WithTimeout(ctx, bootstrapManifestTimeout)
	manifest, err := m.Inspect(checkCtx)
	cancel()
	if err != nil {
		return CompatibilityResult{}, err
	}
	result := CompatibilityResult{ManifestVersion: manifest.Version}
	if manifest.MinimumNodeProtocol == 0 || localProtocol >= manifest.MinimumNodeProtocol {
		return result, nil
	}
	result.Required = true
	request := Request{
		NodeID: m.config.NodeID, UpdateID: "bootstrap-" + manifest.SHA256[:16],
		Version: manifest.Version, ManifestSHA256: manifest.SHA256,
	}
	_ = os.Remove(filepath.Join(m.config.InstallRoot, "update-status.json"))
	if err := m.installVerified(ctx, request, manifest); err != nil {
		m.persistBootstrapFailure(request, err)
		return result, err
	}
	if m.config.Watchdog != nil {
		err = m.config.Watchdog(request)
	} else {
		err = m.startWatchdog(request)
	}
	if err != nil {
		_, _ = restorePending(filepath.Join(m.config.InstallRoot, "update-pending.json"), request.UpdateID)
		m.persistBootstrapFailure(request, err)
		return result, err
	}
	result.ActivationPath = m.config.ActivationPath
	return result, nil
}

func (m *Manager) persistBootstrapFailure(request Request, err error) {
	status := Status{
		NodeID: m.config.NodeID, UpdateID: request.UpdateID, Version: request.Version,
		Phase: PhaseFailed, Error: shortError(err),
	}
	data, marshalErr := json.Marshal(status)
	if marshalErr != nil || os.MkdirAll(m.config.InstallRoot, 0o700) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(m.config.InstallRoot, "update-status.json"), data, 0o600)
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
	if err == nil {
		m.setStatus(request, PhaseDownloading, 8, 0, 0)
	}
	if err == nil && !strings.EqualFold(manifest.SHA256, request.ManifestSHA256) {
		err = errors.New("manifest changed after cluster update began")
	}
	if err == nil && manifest.Version != request.Version {
		err = errors.New("manifest version does not match cluster update")
	}
	if err == nil {
		err = m.installVerified(ctx, request, manifest)
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
		m.status.Phase, m.status.Progress, m.status.Error = PhaseFailed, max(m.status.Progress, 1), shortError(err)
		m.status.UpdatedAt = time.Now().UTC()
		m.mu.Unlock()
		return
	}
	m.status.Phase, m.status.Progress, m.status.UpdatedAt = PhaseRestarting, 95, time.Now().UTC()
	m.mu.Unlock()
	m.config.Restart(m.config.ActivationPath)
}

func (m *Manager) installVerified(
	ctx context.Context,
	request Request,
	manifest VerifiedManifest,
) error {
	artifact, ok := manifest.Artifact(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("release has no artifact for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return m.downloadAndActivate(
		ctx, request, artifact, manifest.MinimumNodeProtocol,
	)
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
