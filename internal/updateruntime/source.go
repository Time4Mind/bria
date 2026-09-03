// Package updateruntime provides local production adapters for updateflow.
// It does not choose an update source or schedule and performs no work until
// called by composition.
package updateruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"bria/internal/update"
	"bria/internal/updateflow"
)

var (
	ErrInvalidSource     = errors.New("invalid release source")
	ErrSourceTooLarge    = errors.New("release source exceeds configured limit")
	ErrRedirectDowngrade = errors.New("release redirect downgraded from HTTPS")
)

type SourceConfig struct {
	ManifestURL        string
	Client             *http.Client
	TemporaryDirectory string
	MaxManifestBytes   int64
	MaxArtifactBytes   int64
}

type ReleaseSource struct {
	manifestURL *url.URL
	client      *http.Client
	temporary   string
	maxManifest int64
	maxArtifact int64
}

func NewReleaseSource(config SourceConfig) (*ReleaseSource, error) {
	if config.MaxManifestBytes <= 0 || config.MaxArtifactBytes <= 0 {
		return nil, ErrInvalidSource
	}
	manifestURL, err := url.Parse(config.ManifestURL)
	if err != nil || manifestURL.Fragment != "" || manifestURL.User != nil {
		return nil, ErrInvalidSource
	}
	source := &ReleaseSource{manifestURL: manifestURL, maxManifest: config.MaxManifestBytes, maxArtifact: config.MaxArtifactBytes}
	switch manifestURL.Scheme {
	case "file":
		if manifestURL.Host != "" || manifestURL.RawQuery != "" || !filepath.IsAbs(manifestURL.Path) {
			return nil, ErrInvalidSource
		}
	case "https":
		if manifestURL.Host == "" || config.Client == nil || config.TemporaryDirectory == "" || !filepath.IsAbs(config.TemporaryDirectory) {
			return nil, ErrInvalidSource
		}
		if err := os.MkdirAll(config.TemporaryDirectory, 0o700); err != nil {
			return nil, fmt.Errorf("create release temporary directory: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(config.TemporaryDirectory))
		if err != nil {
			return nil, fmt.Errorf("resolve release temporary directory: %w", err)
		}
		if !privateRuntimeDirectory(resolved) {
			return nil, ErrInvalidSource
		}
		source.temporary = resolved
		client := *config.Client
		priorRedirect := client.CheckRedirect
		client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			if request.URL.Scheme != "https" {
				return ErrRedirectDowngrade
			}
			if priorRedirect != nil {
				return priorRedirect(request, via)
			}
			if len(via) >= 10 {
				return errors.New("too many release redirects")
			}
			return nil
		}
		source.client = &client
	default:
		return nil, ErrInvalidSource
	}
	return source, nil
}

func (s *ReleaseSource) SignedManifest(ctx context.Context) ([]byte, error) {
	if s == nil || s.manifestURL == nil {
		return nil, ErrInvalidSource
	}
	if s.manifestURL.Scheme == "file" {
		return readRegularBounded(s.manifestURL.Path, s.maxManifest)
	}
	response, err := s.get(ctx, s.manifestURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return readBounded(response.Body, response.ContentLength, s.maxManifest)
}

func (s *ReleaseSource) Artifact(ctx context.Context, artifact update.Artifact) (updateflow.ArtifactPayload, error) {
	if s == nil || s.manifestURL == nil || invalidArtifact(artifact, s.maxArtifact) {
		return nil, ErrInvalidSource
	}
	artifactURL := *s.manifestURL
	artifactURL.RawQuery = ""
	artifactURL.Path = strings.TrimSuffix(filepath.ToSlash(filepath.Dir(s.manifestURL.Path)), "/") + "/" + artifact.Name
	artifactURL.RawPath = ""
	if artifactURL.Scheme == "file" {
		return openRegularExact(filepath.FromSlash(artifactURL.Path), artifact.Size, s.maxArtifact)
	}
	response, err := s.get(ctx, &artifactURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.ContentLength > s.maxArtifact || (response.ContentLength >= 0 && response.ContentLength != artifact.Size) {
		return nil, ErrSourceTooLarge
	}
	temporary, err := os.CreateTemp(s.temporary, ".release-artifact-*")
	if err != nil {
		return nil, fmt.Errorf("create release artifact: %w", err)
	}
	path := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(path)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("protect release artifact: %w", err)
	}
	written, err := io.Copy(temporary, io.LimitReader(response.Body, s.maxArtifact+1))
	if err != nil || written > s.maxArtifact || written != artifact.Size {
		cleanup()
		if err != nil {
			return nil, fmt.Errorf("read release artifact: %w", err)
		}
		return nil, ErrSourceTooLarge
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return nil, fmt.Errorf("sync release artifact: %w", err)
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, fmt.Errorf("rewind release artifact: %w", err)
	}
	return &temporaryPayload{File: temporary, path: path}, nil
}

func (s *ReleaseSource) get(ctx context.Context, target *url.URL) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, ErrInvalidSource
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.Request == nil || response.Request.URL.Scheme != "https" || response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, ErrInvalidSource
	}
	return response, nil
}

func readRegularBounded(path string, maximum int64) ([]byte, error) {
	file, err := openRegularExact(path, -1, maximum)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBounded(file, file.Size(), maximum)
}

func readBounded(reader io.Reader, declared, maximum int64) ([]byte, error) {
	if declared > maximum {
		return nil, ErrSourceTooLarge
	}
	content, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, ErrSourceTooLarge
	}
	return content, nil
}

type regularPayload struct {
	*os.File
	size int64
}

func (p *regularPayload) Size() int64 { return p.size }

func openRegularExact(path string, expected, maximum int64) (*regularPayload, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum || (expected >= 0 && info.Size() != expected) {
		return nil, ErrInvalidSource
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalidSource
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() > maximum ||
		(expected >= 0 && opened.Size() != expected) {
		file.Close()
		return nil, ErrInvalidSource
	}
	return &regularPayload{File: file, size: opened.Size()}, nil
}

type temporaryPayload struct {
	*os.File
	path string
}

func (p *temporaryPayload) Close() error {
	closeErr := p.File.Close()
	removeErr := os.Remove(p.path)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func invalidArtifact(artifact update.Artifact, maximum int64) bool {
	return artifact.Name == "" || artifact.Name != filepath.Base(artifact.Name) || artifact.Size < 0 || artifact.Size > maximum
}
