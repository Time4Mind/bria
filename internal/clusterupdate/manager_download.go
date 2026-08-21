package clusterupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func (m *Manager) downloadAndActivate(
	ctx context.Context,
	request Request,
	artifact Artifact,
	minimumNodeProtocol int,
) error {
	if err := os.MkdirAll(filepath.Join(m.config.InstallRoot, "releases"), 0o700); err != nil {
		return fmt.Errorf("create update release root: %w", err)
	}
	temporary, err := os.CreateTemp(m.config.InstallRoot, ".download-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create update download: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := m.download(ctx, request, temporary, artifact); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	m.setStatus(request, PhaseVerifying, 60, artifact.Size, artifact.Size)
	releasesRoot := filepath.Join(m.config.InstallRoot, "releases")
	candidateFile, err := os.CreateTemp(releasesRoot, ".candidate-*.reserve")
	if err != nil {
		return err
	}
	candidate := candidateFile.Name()
	if err := candidateFile.Close(); err != nil {
		return err
	}
	if err := os.Remove(candidate); err != nil {
		return err
	}
	defer func() {
		_ = makeOwnedTreeRemovable(candidate)
		_ = os.RemoveAll(candidate)
	}()
	m.setStatus(request, PhaseExtracting, 68, artifact.Size, artifact.Size)
	if err := extractRelease(temporaryPath, candidate); err != nil {
		return err
	}
	m.setStatus(request, PhaseVerifying, 76, artifact.Size, artifact.Size)
	binarySHA256, err := verifyReleaseBinary(candidate, request.Version, minimumNodeProtocol, artifact.SHA256)
	if err != nil {
		return err
	}
	destination := filepath.Join(releasesRoot, binarySHA256)
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(candidate, destination); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if err := verifyInstalledReleaseMetadata(destination); err != nil {
			return err
		}
		if equal, err := sameRuntimeReleasePayload(candidate, destination); err != nil || !equal {
			if err != nil {
				return err
			}
			return errors.New("existing release payload does not match candidate")
		}
	}
	m.setStatus(request, PhasePreflight, 84, artifact.Size, artifact.Size)
	if err := m.config.Preflight(ctx, releaseBinary(destination)); err != nil {
		return fmt.Errorf("preflight staged Bria: %w", err)
	}
	m.setStatus(request, PhaseActivating, 90, artifact.Size, artifact.Size)
	return m.switchCurrent(request, destination)
}

func (m *Manager) download(
	ctx context.Context, update Request, destination *os.File, artifact Artifact,
) error {
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
	progress := &downloadProgress{manager: m, request: update, total: artifact.Size, last: -1}
	written, err := io.Copy(
		io.MultiWriter(destination, hash, progress), io.LimitReader(response.Body, artifact.Size+1),
	)
	if err != nil || written != artifact.Size || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return errors.New("update artifact size or digest mismatch")
	}
	return destination.Sync()
}

type downloadProgress struct {
	manager *Manager
	request Request
	total   int64
	written int64
	last    int
}

func (p *downloadProgress) Write(data []byte) (int, error) {
	p.written += int64(len(data))
	progress := 8
	if p.total > 0 {
		progress += int(47 * min(p.written, p.total) / p.total)
	}
	if progress != p.last {
		p.last = progress
		p.manager.setStatus(p.request, PhaseDownloading, progress, p.written, p.total)
	}
	return len(data), nil
}
