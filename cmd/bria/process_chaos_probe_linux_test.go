//go:build linux

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
)

func chaosProbeClient(
	t *testing.T,
	configs []config.Config,
	certificate tls.Certificate,
	roots *x509.CertPool,
) *nodecontrol.Client {
	t.Helper()
	addresses := make(map[string]string, len(configs))
	for _, item := range configs {
		addresses[item.NodeID] = item.ControlAdvertise
	}
	client, err := nodecontrol.NewClient(nodecontrol.ClientConfig{
		Certificate: certificate, Roots: roots, ClusterID: "chaos",
		Resolver: nodecontrol.NewStaticResolver(addresses), Timeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func waitForSingleProcessLeader(
	t *testing.T,
	client *nodecontrol.Client,
	configs []config.Config,
	timeout time.Duration,
) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		leader := ""
		actualLeader := ""
		consistent := true
		ready := 0
		for _, item := range configs {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			status, err := client.Probe(ctx, item.NodeID, true)
			cancel()
			if err != nil || status.Status != "ready" {
				continue
			}
			ready++
			if leader == "" {
				leader = status.LeaderID
			} else if leader != status.LeaderID {
				consistent = false
			}
			if status.RaftState == "Leader" && status.LeaderID == item.NodeID {
				if actualLeader != "" && actualLeader != item.NodeID {
					consistent = false
				}
				actualLeader = item.NodeID
			}
		}
		if ready >= 2 && consistent && leader != "" && actualLeader == leader {
			return actualLeader
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("process cluster did not converge on one leader")
	return ""
}

func waitForAllReady(
	t *testing.T,
	client *nodecontrol.Client,
	configs []config.Config,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready := 0
		for _, item := range configs {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			status, err := client.Probe(ctx, item.NodeID, true)
			cancel()
			if err == nil && status.Status == "ready" {
				ready++
			}
		}
		if ready == len(configs) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("not all node processes became ready")
}

func waitForNodeNotReady(
	t *testing.T,
	client *nodecontrol.Client,
	nodeID string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		status, err := client.Probe(ctx, nodeID, true)
		cancel()
		if err == nil && status.Status == "not_ready" && status.LeaderID == "" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("node %s retained readiness without quorum", nodeID)
}

func waitForNodeReadyWithLeader(
	t *testing.T,
	client *nodecontrol.Client,
	nodeID string,
	leaderID string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		status, err := client.Probe(ctx, nodeID, true)
		cancel()
		if err == nil && status.Status == "ready" && status.LeaderID == leaderID {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("node %s did not remain ready with leader %s", nodeID, leaderID)
}

func configByID(t *testing.T, configs []config.Config, nodeID string) config.Config {
	t.Helper()
	for _, item := range configs {
		if item.NodeID == nodeID {
			return item
		}
	}
	t.Fatalf("unknown node %s", nodeID)
	return config.Config{}
}
