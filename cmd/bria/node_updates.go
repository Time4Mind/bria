package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
)

func newNodeUpdateServices(
	nodeConfig config.Config,
	configPath string,
	client *nodecontrol.Client,
	restarts chan<- string,
) (*clusterupdate.Manager, *nodecontrol.UpdateRouter, error) {
	local, err := newLocalUpdateManager(nodeConfig, configPath, func(binary string) {
		select {
		case restarts <- binary:
		default:
		}
	}, preflightUpdateCandidate)
	if err != nil || local == nil {
		return local, nil, err
	}
	remote, err := nodecontrol.NewUpdateClient(client)
	if err != nil {
		return nil, nil, err
	}
	router, err := nodecontrol.NewUpdateRouter(nodeConfig.NodeID, local, remote)
	if err != nil {
		return nil, nil, err
	}
	return local, router, nil
}

func newLocalUpdateManager(
	nodeConfig config.Config,
	configPath string,
	restart func(string),
	preflight func(context.Context, string, string) error,
) (*clusterupdate.Manager, error) {
	if nodeConfig.UpdateManifestURL == "" {
		return nil, nil
	}
	publicKey, err := clusterupdate.DecodePublicKey(nodeConfig.UpdatePublicKey)
	if err != nil {
		return nil, err
	}
	httpClient, err := updateHTTPClient(nodeConfig.TelegramProxyURL())
	if err != nil {
		return nil, err
	}
	installRoot, activationPath, runningPath, err := localInstallPaths(nodeConfig)
	if err != nil {
		return nil, err
	}
	return clusterupdate.NewManager(clusterupdate.ManagerConfig{
		NodeID: nodeConfig.NodeID, InstallRoot: installRoot, ActivationPath: activationPath,
		RunningPath: runningPath,
		Fetcher: clusterupdate.Fetcher{
			URL: nodeConfig.UpdateManifestURL, PublicKey: publicKey, Client: httpClient,
		},
		Client: httpClient,
		Preflight: func(ctx context.Context, binary string) error {
			return preflight(ctx, binary, configPath)
		},
		Restart: restart,
	})
}

type localReleaseCleaner struct {
	installRoot, activationPath, runningPath string
}

func (cleaner localReleaseCleaner) CleanupArtifacts(now time.Time) (clusterupdate.CleanupReport, error) {
	return clusterupdate.CleanupUpdateArtifacts(
		cleaner.installRoot, cleaner.activationPath, now, cleaner.runningPath,
	)
}

func newLocalReleaseCleaner(nodeConfig config.Config) (localReleaseCleaner, error) {
	installRoot, activationPath, runningPath, err := localInstallPaths(nodeConfig)
	return localReleaseCleaner{
		installRoot: installRoot, activationPath: activationPath, runningPath: runningPath,
	}, err
}

func localInstallPaths(nodeConfig config.Config) (string, string, string, error) {
	activationPath, err := resolveActivationPath()
	if err != nil {
		return "", "", "", err
	}
	installRoot := nodeConfig.EffectiveUpdateInstallRoot()
	if filepath.Base(filepath.Dir(activationPath)) == "current" && nodeConfig.UpdateInstallRoot == "" {
		installRoot = filepath.Dir(filepath.Dir(activationPath))
	}
	executable, err := os.Executable()
	if err != nil {
		return "", "", "", errors.New("resolve running Bria executable")
	}
	runningPath, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "", "", errors.New("resolve immutable running Bria release")
	}
	runningPath, err = filepath.Abs(runningPath)
	if err != nil {
		return "", "", "", errors.New("resolve immutable running Bria release")
	}
	return installRoot, activationPath, runningPath, nil
}

func bootstrapNodeCompatibility(
	ctx context.Context,
	nodeConfig config.Config,
	configPath string,
) (string, error) {
	manager, err := newLocalUpdateManager(
		nodeConfig, configPath, func(string) {}, preflightBootstrapCandidate,
	)
	if err != nil || manager == nil {
		return "", err
	}
	result, err := manager.EnsureCompatible(ctx, clusterupdate.NodeProtocolVersion)
	if err != nil {
		if !result.Required {
			return "", fmt.Errorf("verify signed compatibility manifest: %w", err)
		}
		return "", fmt.Errorf(
			"install required compatibility update %s: %w", result.ManifestVersion, err,
		)
	}
	return result.ActivationPath, nil
}

func preflightBootstrapCandidate(ctx context.Context, binary, configPath string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(
		probeCtx, binary, "node", "config-check", "--config", configPath,
	).CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.Join(strings.Fields(string(output)), " ")
	if detail == "" {
		detail = err.Error()
	}
	if len(detail) > 240 {
		detail = detail[:240]
	}
	return fmt.Errorf("candidate cannot load the current node config: %s", detail)
}

func preflightUpdateCandidate(ctx context.Context, binary, configPath string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(
		probeCtx, binary, "node", "probe", "--config", configPath, "--health-only",
	).CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.Join(strings.Fields(string(output)), " ")
	if detail == "" {
		detail = err.Error()
	}
	if len(detail) > 240 {
		detail = detail[:240]
	}
	return fmt.Errorf("candidate cannot use the current node config: %s", detail)
}

type nodeUpdateControl struct {
	local    *clusterupdate.Manager
	router   *nodecontrol.UpdateRouter
	restarts chan string
}

func prepareNodeUpdates(
	nodeConfig config.Config, configPath string, client *nodecontrol.Client,
) (*nodeUpdateControl, error) {
	restarts := make(chan string, 1)
	local, router, err := newNodeUpdateServices(nodeConfig, configPath, client, restarts)
	if err != nil {
		return nil, err
	}
	return &nodeUpdateControl{local: local, router: router, restarts: restarts}, nil
}

func resolveActivationPath() (string, error) {
	value := os.Args[0]
	if !strings.ContainsRune(value, filepath.Separator) {
		resolved, err := exec.LookPath(value)
		if err != nil {
			return "", err
		}
		value = resolved
	}
	return filepath.Abs(value)
}

func updateHTTPClient(rawProxyURL string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if rawProxyURL != "" {
		proxyURL, err := url.Parse(rawProxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Minute}, nil
}
