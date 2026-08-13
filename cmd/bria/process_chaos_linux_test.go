//go:build linux

package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/security"
)

const processChaosEnvironment = "BRIA_PROCESS_CHAOS"

func TestBriaNodeProcessHelper(t *testing.T) {
	path := os.Getenv("BRIA_NODE_HELPER_CONFIG")
	if path == "" {
		t.Skip("subprocess helper")
	}
	if err := runNode([]string{"run", "--config", path}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessChaosLeaderFailureAndDiskRejoin(t *testing.T) {
	if os.Getenv(processChaosEnvironment) != "1" {
		t.Skip("set BRIA_PROCESS_CHAOS=1 to run process-level chaos")
	}
	root := t.TempDir()
	prependFakeTmux(t, root)
	configs, certificates, roots := writeChaosCluster(t, root, 3)
	processes := make(map[string]*chaosProcess, len(configs))
	for _, item := range configs {
		processes[item.NodeID] = startChaosNode(t, root, item)
	}
	t.Cleanup(func() {
		for _, process := range processes {
			process.stop(t)
		}
	})
	client := chaosProbeClient(t, configs, certificates["node-1"], roots)
	leader := waitForSingleProcessLeader(t, client, configs, 35*time.Second)
	processes[leader].kill(t)
	newLeader := waitForSingleProcessLeader(t, client, configs, 20*time.Second)
	if newLeader == leader {
		t.Fatal("abruptly stopped node remained leader")
	}
	processes[leader] = startChaosNode(t, root, configByID(t, configs, leader))
	waitForAllReady(t, client, configs, 20*time.Second)
	if got := waitForSingleProcessLeader(t, client, configs, 5*time.Second); got != newLeader {
		t.Fatalf("rejoining node disrupted leader: got %s, want %s", got, newLeader)
	}
}

func TestProcessChaosQuorumLossRejectsReadiness(t *testing.T) {
	if os.Getenv(processChaosEnvironment) != "1" {
		t.Skip("set BRIA_PROCESS_CHAOS=1 to run process-level chaos")
	}
	root := t.TempDir()
	prependFakeTmux(t, root)
	configs, certificates, roots := writeChaosCluster(t, root, 3)
	processes := make(map[string]*chaosProcess, len(configs))
	for _, item := range configs {
		processes[item.NodeID] = startChaosNode(t, root, item)
	}
	t.Cleanup(func() {
		for _, process := range processes {
			process.stop(t)
		}
	})
	client := chaosProbeClient(t, configs, certificates["node-1"], roots)
	leader := waitForSingleProcessLeader(t, client, configs, 35*time.Second)
	stopped := 0
	for _, item := range configs {
		if item.NodeID == leader || stopped >= 2 {
			continue
		}
		processes[item.NodeID].kill(t)
		stopped++
	}
	waitForNodeNotReady(t, client, leader, 10*time.Second)
}

type chaosProcess struct {
	command *exec.Cmd
	done    chan error
	log     *os.File
}

func startChaosNode(t *testing.T, root string, item config.Config) *chaosProcess {
	t.Helper()
	logFile, err := os.Create(filepath.Join(root, item.NodeID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestBriaNodeProcessHelper$")
	command.Env = append(os.Environ(), "BRIA_NODE_HELPER_CONFIG="+filepath.Join(item.DataDir, "config.json"))
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	process := &chaosProcess{command: command, done: make(chan error, 1), log: logFile}
	go func() { process.done <- command.Wait() }()
	return process
}

func (p *chaosProcess) kill(t *testing.T) {
	t.Helper()
	if p == nil || p.command.ProcessState != nil {
		return
	}
	if err := p.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("killed node process did not exit")
	}
	_ = p.log.Close()
}

func (p *chaosProcess) stop(t *testing.T) {
	t.Helper()
	if p == nil || p.command.ProcessState != nil {
		return
	}
	_ = p.command.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		p.kill(t)
		return
	}
	_ = p.log.Close()
}

func prependFakeTmux(t *testing.T, root string) {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bin, "tmux")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeChaosCluster(
	t *testing.T,
	root string,
	count int,
) ([]config.Config, map[string]tls.Certificate, *x509.CertPool) {
	t.Helper()
	now := time.Now().UTC()
	ca, caPEM, caKey, err := security.GenerateCA("chaos", now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	peers := make([]config.RaftPeer, 0, count)
	for index := 1; index <= count; index++ {
		peers = append(peers, config.RaftPeer{
			NodeID: fmt.Sprintf("node-%d", index), NodeName: fmt.Sprintf("Node %d", index),
			Address: reserveTCPAddress(t), ControlAddress: reserveTCPAddress(t),
		})
	}
	configs := make([]config.Config, 0, count)
	certificates := make(map[string]tls.Certificate, count)
	for index, peer := range peers {
		item := chaosConfig(t, root, peer, peers, index == 0)
		credentials, err := security.IssueNodeCertificate(ca, "chaos", peer.NodeID, now, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		certificate, err := tls.X509KeyPair(credentials.CertificatePEM, credentials.PrivateKeyPEM)
		if err != nil {
			t.Fatal(err)
		}
		certificates[peer.NodeID] = certificate
		writeChaosFile(t, item.CACertificate, caPEM)
		writeChaosFile(t, item.NodeCertificate, credentials.CertificatePEM)
		writeChaosFile(t, item.NodePrivateKey, credentials.PrivateKeyPEM)
		writeChaosFile(t, item.CallbackKeyFile, make([]byte, callbacktoken.KeyBytes))
		if item.Bootstrap {
			writeChaosFile(t, item.CAPrivateKey, caKey)
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		writeChaosFile(t, filepath.Join(item.DataDir, "config.json"), encoded)
		configs = append(configs, item)
	}
	return configs, certificates, roots
}

func chaosConfig(
	t *testing.T,
	root string,
	peer config.RaftPeer,
	peers []config.RaftPeer,
	bootstrap bool,
) config.Config {
	t.Helper()
	dataDir := filepath.Join(root, peer.NodeID)
	pki := filepath.Join(dataDir, "pki")
	secrets := filepath.Join(dataDir, "secrets")
	for _, directory := range []string{dataDir, pki, secrets} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	item := config.Config{
		ClusterID: "chaos", NodeID: peer.NodeID, NodeName: peer.NodeName,
		DataDir: dataDir, RaftBind: peer.Address, RaftAdvertise: peer.Address,
		ControlBind: peer.ControlAddress, ControlAdvertise: peer.ControlAddress,
		EnrollmentBind: reserveTCPAddress(t), EnrollmentAdvertise: reserveTCPAddress(t),
		EnrollmentIssuerID: peers[0].NodeID, RaftPeers: append([]config.RaftPeer(nil), peers...),
		Bootstrap: bootstrap, CACertificate: filepath.Join(pki, "ca.crt"),
		NodeCertificate: filepath.Join(pki, "node.crt"), NodePrivateKey: filepath.Join(pki, "node.key"),
		TelegramTokenFile: filepath.Join(secrets, "telegram.token"),
		CallbackKeyFile:   filepath.Join(secrets, "callback.key"), TmuxSession: "bria-chaos",
		ClaudeCommand: "true", ClaudeNamingModel: "none", CodexCommand: "true",
		CodexNamingModel: "none", SpeechEngine: config.SpeechEngineWhisper,
		FFmpegCommand: "true", WhisperCommand: "true", WhisperLanguage: "auto", WhisperThreads: 1,
	}
	if bootstrap {
		item.CAPrivateKey = filepath.Join(pki, "ca.key")
	}
	if err := item.Validate(); err != nil {
		t.Fatal(err)
	}
	return item
}

func writeChaosFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func reserveTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
