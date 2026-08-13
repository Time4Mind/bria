package nodecontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/security"
)

type providerAuthRecorder struct{ starts int }

func (r *providerAuthRecorder) Start(
	_ context.Context,
	request providerauth.StartRequest,
) (providerauth.Status, error) {
	r.starts++
	return providerauth.Status{
		FlowID: "abcdefghijklmnopqrstuvwx", Backend: request.Backend,
		State: providerauth.StateWaitingUser, ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}
func (*providerAuthRecorder) Submit(context.Context, providerauth.SubmitRequest) (providerauth.Status, error) {
	return providerauth.Status{}, nil
}
func (*providerAuthRecorder) Status(context.Context, providerauth.FlowRequest) (providerauth.Status, error) {
	return providerauth.Status{}, nil
}
func (*providerAuthRecorder) Cancel(context.Context, providerauth.FlowRequest) error { return nil }

func TestProviderAuthServerAcceptsOnlyCurrentLeaderAndExactTarget(t *testing.T) {
	state := controlState(t)
	node := state.Nodes["target"]
	node.Backends = []domain.BackendDescriptor{{Name: "codex"}}
	state.Nodes["target"] = node
	guard, _ := NewStateGuard(staticState{state})
	executor := &fakeExecutor{}
	runtimeService, _ := NewService("target", guard, executor)
	ca, caPEM, _, _ := security.GenerateCA("cluster", time.Now(), time.Hour)
	target := issueTLSCertificate(t, ca, "cluster", "target")
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, _ := security.CertificatePool(caPEM)
	providerAuth := &providerAuthRecorder{}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate, Roots: roots,
		Leadership: staticLeadership("leader"), Membership: guard, Service: runtimeService,
		ProviderAuth: providerAuth,
	})
	if err != nil {
		t.Fatal(err)
	}

	requestBody := providerauth.StartRequest{ActorID: 1, NodeID: "target", Backend: "codex"}
	recorder := providerAuthStartRequest(t, server, leader.LeafCertificate, requestBody)
	if recorder.Code != http.StatusOK || providerAuth.starts != 1 {
		t.Fatalf("status=%d starts=%d body=%q", recorder.Code, providerAuth.starts, recorder.Body.String())
	}
	server.leadership = staticLeadership("target")
	recorder = providerAuthStartRequest(t, server, leader.LeafCertificate, requestBody)
	if recorder.Code != http.StatusConflict || providerAuth.starts != 1 {
		t.Fatalf("stale leader status=%d starts=%d", recorder.Code, providerAuth.starts)
	}
	server.leadership = staticLeadership("leader")
	requestBody.NodeID = "leader"
	recorder = providerAuthStartRequest(t, server, leader.LeafCertificate, requestBody)
	if recorder.Code != http.StatusConflict || providerAuth.starts != 1 {
		t.Fatalf("wrong target status=%d starts=%d", recorder.Code, providerAuth.starts)
	}
}

func providerAuthStartRequest(
	t *testing.T,
	server *Server,
	peer []*x509.Certificate,
	body providerauth.StartRequest,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, providerAuthStartPath, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: peer}
	recorder := httptest.NewRecorder()
	server.handleProviderAuthStart(recorder, request)
	return recorder
}

func TestStateGuardProviderAuthIsOwnerOnlineBackendOnly(t *testing.T) {
	state := controlState(t)
	node := state.Nodes["target"]
	node.Backends = []domain.BackendDescriptor{{Name: "codex"}}
	state.Nodes["target"] = node
	guard, _ := NewStateGuard(staticState{state})
	if err := guard.AuthorizeProviderAuth(context.Background(), 1, "target", "CODEX"); err != nil {
		t.Fatal(err)
	}
	if err := guard.AuthorizeProviderAuth(context.Background(), 2, "target", "codex"); err == nil {
		t.Fatal("non-owner provider auth succeeded")
	}
	if err := guard.AuthorizeProviderAuth(context.Background(), 1, "target", "unknown"); err == nil {
		t.Fatal("unknown backend provider auth succeeded")
	}
}
