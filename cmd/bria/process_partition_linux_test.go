//go:build linux

package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/config"
)

func TestProcessChaosManualLeaderSurvivesReplicaPartition(t *testing.T) {
	if !processChaosEnabled() {
		t.Skip("set BRIA_PROCESS_CHAOS=1 to run process-level chaos")
	}
	root := t.TempDir()
	prependFakeTmux(t, root)
	configs, certificates, roots, relays := writeRelayedChaosCluster(t, root, 2)
	processes := make(map[string]*chaosProcess, len(configs))
	for _, item := range configs {
		processes[item.NodeID] = startChaosNode(t, root, item)
	}
	t.Cleanup(func() {
		for _, process := range processes {
			process.stop(t)
		}
		for _, relay := range relays {
			relay.close()
		}
	})
	client := chaosProbeClient(t, configs, certificates["node-1"], roots)
	leader := waitForSingleProcessLeader(t, client, configs, 35*time.Second)
	for _, relay := range relays {
		relay.pause()
	}
	waitForNodeReadyWithLeader(t, client, leader, leader, 10*time.Second)
	assertNoReplacementProcessLeader(t, client, configs, leader, 3*time.Second)
	for _, relay := range relays {
		relay.resume()
	}
	waitForAllReady(t, client, configs, 20*time.Second)
	if got := waitForSingleProcessLeader(t, client, configs, 5*time.Second); got != leader {
		t.Fatalf("partition changed manual leader: got %s, want %s", got, leader)
	}
}

func processChaosEnabled() bool {
	return os.Getenv(processChaosEnvironment) == "1"
}

func writeRelayedChaosCluster(
	t *testing.T,
	root string,
	count int,
) ([]config.Config, map[string]tls.Certificate, *x509.CertPool, []*tcpRelay) {
	t.Helper()
	binds := make([]string, count)
	relays := make([]*tcpRelay, count)
	for index := range count {
		binds[index] = reserveTCPAddress(t)
		relays[index] = newTCPRelay(t, binds[index])
	}
	configs, certificates, roots := writeChaosCluster(t, root, count)
	for index := range configs {
		oldAddress := configs[index].RaftAdvertise
		advertise := relays[index].address()
		configs[index].RaftBind = binds[index]
		configs[index].RaftAdvertise = advertise
		for peerIndex := range configs[index].RaftPeers {
			if configs[index].RaftPeers[peerIndex].Address == oldAddress {
				configs[index].RaftPeers[peerIndex].Address = advertise
			}
		}
	}
	for index := range configs {
		for peerIndex := range configs[index].RaftPeers {
			configs[index].RaftPeers[peerIndex].Address = relays[peerIndex].address()
		}
		encoded, err := json.Marshal(configs[index])
		if err != nil {
			t.Fatal(err)
		}
		writeChaosFile(t, filepath.Join(configs[index].DataDir, "config.json"), encoded)
	}
	return configs, certificates, roots, relays
}

type tcpRelay struct {
	target        string
	listenAddress string

	mu          sync.Mutex
	listener    net.Listener
	connections map[net.Conn]struct{}
	closed      bool
}

func newTCPRelay(t *testing.T, target string) *tcpRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relay := &tcpRelay{
		target: target, listenAddress: listener.Addr().String(), listener: listener,
		connections: map[net.Conn]struct{}{},
	}
	go relay.accept(listener)
	return relay
}

func (r *tcpRelay) address() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listener == nil {
		return ""
	}
	return r.listenAddress
}

func (r *tcpRelay) pause() {
	r.mu.Lock()
	listener := r.listener
	r.listener = nil
	connections := make([]net.Conn, 0, len(r.connections))
	for connection := range r.connections {
		connections = append(connections, connection)
	}
	r.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (r *tcpRelay) resume() {
	r.mu.Lock()
	if r.closed || r.listener != nil {
		r.mu.Unlock()
		return
	}
	listener, err := net.Listen("tcp", r.listenAddress)
	if err != nil {
		r.mu.Unlock()
		return
	}
	r.listener = listener
	r.mu.Unlock()
	go r.accept(listener)
}

func (r *tcpRelay) accept(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go r.forward(connection)
	}
}

func (r *tcpRelay) forward(inbound net.Conn) {
	outbound, err := net.DialTimeout("tcp", r.target, time.Second)
	if err != nil {
		_ = inbound.Close()
		return
	}
	r.track(inbound, outbound)
	defer r.untrack(inbound, outbound)
	done := make(chan struct{}, 2)
	go copyRelay(outbound, inbound, done)
	go copyRelay(inbound, outbound, done)
	<-done
	_ = inbound.Close()
	_ = outbound.Close()
	<-done
}

func (r *tcpRelay) track(connections ...net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, connection := range connections {
		r.connections[connection] = struct{}{}
	}
}

func (r *tcpRelay) untrack(connections ...net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, connection := range connections {
		delete(r.connections, connection)
	}
}

func (r *tcpRelay) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.pause()
}

func copyRelay(destination io.Writer, source io.Reader, done chan<- struct{}) {
	_, _ = io.Copy(destination, source)
	done <- struct{}{}
}
