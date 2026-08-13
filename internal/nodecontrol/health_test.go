package nodecontrol

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/security"
)

type healthObserver struct {
	leader string
	stats  map[string]string
}

func TestClientProbeUsesMutualTLSAndReportsReadiness(t *testing.T) {
	state := controlState(t)
	guard, _ := NewStateGuard(staticState{state})
	ca, caPEM, _, _ := security.GenerateCA("cluster", time.Now(), time.Hour)
	target := issueTLSCertificate(t, ca, "cluster", "target")
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, _ := security.CertificatePool(caPEM)
	observer := healthObserver{leader: "leader", stats: map[string]string{"state": "Follower"}}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate,
		Roots: roots, Leadership: observer, Health: observer,
		Membership: guard, Service: &submitRecorder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener := newHealthListener(t, server)
	client, err := NewClient(ClientConfig{
		Certificate: leader.Certificate, Roots: roots, ClusterID: "cluster",
		Resolver: NewStaticResolver(map[string]string{"target": listener.Addr().String()}),
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	status, err := client.Probe(context.Background(), "target", true)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "ready" || status.NodeID != "target" || status.LeaderID != "leader" {
		t.Fatalf("status=%#v", status)
	}
}

func newHealthListener(t *testing.T, server *Server) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	go func() { _ = server.Serve(listener) }()
	return listener
}

func (h healthObserver) LeaderID() string         { return h.leader }
func (h healthObserver) Stats() map[string]string { return h.stats }

func TestHealthAndReadinessRequireMemberAndKnownLeader(t *testing.T) {
	state := controlState(t)
	guard, err := NewStateGuard(staticState{state})
	if err != nil {
		t.Fatal(err)
	}
	ca, caPEM, _, err := security.GenerateCA("cluster", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	target := issueTLSCertificate(t, ca, "cluster", "target")
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	observer := healthObserver{leader: "leader", stats: map[string]string{
		"state": "Follower", "applied_index": "42",
	}}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate,
		Roots: roots, Leadership: observer, Health: observer,
		Membership: guard, Service: &submitRecorder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, readinessPath, nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: leader.LeafCertificate}
	recorder := httptest.NewRecorder()
	server.handleReadiness(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	var body HealthStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ready" || body.NodeID != "target" || body.LeaderID != "leader" ||
		body.RaftState != "Follower" || body.AppliedIndex != "42" {
		t.Fatalf("ready body=%#v", body)
	}

	server.health = healthObserver{stats: observer.stats}
	recorder = httptest.NewRecorder()
	server.handleReadiness(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("partitioned ready status=%d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	server.handleHealth(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("partitioned health status=%d", recorder.Code)
	}
	request.TLS = nil
	recorder = httptest.NewRecorder()
	server.handleHealth(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated health status=%d", recorder.Code)
	}
}

func TestMetricsRequireMemberAndExposeOnlyBoundedNodeHealth(t *testing.T) {
	state := controlState(t)
	guard, _ := NewStateGuard(staticState{state})
	ca, caPEM, _, _ := security.GenerateCA("cluster", time.Now(), time.Hour)
	target := issueTLSCertificate(t, ca, "cluster", "target")
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, _ := security.CertificatePool(caPEM)
	observer := healthObserver{leader: "leader", stats: map[string]string{
		"state": "Follower", "applied_index": "42",
	}}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate,
		Roots: roots, Leadership: observer, Health: observer,
		Membership: guard, Service: &submitRecorder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: leader.LeafCertificate}
	recorder := httptest.NewRecorder()
	server.handleMetrics(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, wanted := range []string{
		`bria_process_up{node_id="target"} 1`,
		`bria_node_ready{node_id="target"} 1`,
		`bria_raft_has_leader{node_id="target"} 1`,
		`bria_raft_applied_index{node_id="target"} 42`,
		`bria_raft_state{node_id="target",state="follower"} 1`,
	} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("metrics missing %q:\n%s", wanted, body)
		}
	}
	for _, forbidden := range []string{"leader_id", "session", "telegram", "provider"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("metrics expose forbidden field %q", forbidden)
		}
	}

	request.TLS = nil
	recorder = httptest.NewRecorder()
	server.handleMetrics(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics status=%d", recorder.Code)
	}
}

func TestClientReadsAuthenticatedMetrics(t *testing.T) {
	state := controlState(t)
	guard, _ := NewStateGuard(staticState{state})
	ca, caPEM, _, _ := security.GenerateCA("cluster", time.Now(), time.Hour)
	target := issueTLSCertificate(t, ca, "cluster", "target")
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, _ := security.CertificatePool(caPEM)
	observer := healthObserver{leader: "leader", stats: map[string]string{"state": "Leader"}}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate,
		Roots: roots, Leadership: observer, Health: observer,
		Membership: guard, Service: &submitRecorder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener := newHealthListener(t, server)
	client, err := NewClient(ClientConfig{
		Certificate: leader.Certificate, Roots: roots, ClusterID: "cluster",
		Resolver: NewStaticResolver(map[string]string{"target": listener.Addr().String()}),
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	metrics, err := client.Metrics(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(metrics, `bria_raft_state{node_id="target",state="leader"} 1`) {
		t.Fatalf("unexpected metrics:\n%s", metrics)
	}
}
