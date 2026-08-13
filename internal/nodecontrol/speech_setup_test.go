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

	"github.com/Time4Mind/bria/internal/security"
	"github.com/Time4Mind/bria/internal/speechsetup"
)

type speechSetupRecorder struct{ starts int }

func (r *speechSetupRecorder) Start(
	_ context.Context, request speechsetup.Request,
) (speechsetup.Status, error) {
	r.starts++
	return speechsetup.Status{
		NodeID: request.NodeID, Engine: "whisper", Phase: speechsetup.PhaseInstalling,
	}, nil
}

func (*speechSetupRecorder) Status(
	_ context.Context, request speechsetup.Request,
) (speechsetup.Status, error) {
	return speechsetup.Status{NodeID: request.NodeID, Engine: "whisper"}, nil
}

func TestSpeechSetupAcceptsOnlyCurrentLeaderAndExactTarget(t *testing.T) {
	state := controlState(t)
	guard, _ := NewStateGuard(staticState{state})
	runtimeService, _ := NewService("target", guard, &fakeExecutor{})
	ca, caPEM, _, _ := security.GenerateCA("cluster", time.Now(), time.Hour)
	target := issueTLSCertificate(t, ca, "cluster", "target")
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, _ := security.CertificatePool(caPEM)
	setup := &speechSetupRecorder{}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate, Roots: roots,
		Leadership: staticLeadership("leader"), Membership: guard, Service: runtimeService,
		SpeechSetup: setup,
	})
	if err != nil {
		t.Fatal(err)
	}

	recorder := speechSetupStartRequest(t, server, leader.LeafCertificate, "target")
	if recorder.Code != http.StatusOK || setup.starts != 1 {
		t.Fatalf("leader status=%d starts=%d", recorder.Code, setup.starts)
	}
	server.leadership = staticLeadership("target")
	recorder = speechSetupStartRequest(t, server, leader.LeafCertificate, "target")
	if recorder.Code != http.StatusConflict || setup.starts != 1 {
		t.Fatalf("stale leader status=%d starts=%d", recorder.Code, setup.starts)
	}
	server.leadership = staticLeadership("leader")
	recorder = speechSetupStartRequest(t, server, leader.LeafCertificate, "leader")
	if recorder.Code != http.StatusBadRequest || setup.starts != 1 {
		t.Fatalf("wrong target status=%d starts=%d", recorder.Code, setup.starts)
	}
}

func speechSetupStartRequest(
	t *testing.T, server *Server, peer []*x509.Certificate, nodeID string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(speechsetup.Request{NodeID: nodeID})
	request := httptest.NewRequest(http.MethodPost, speechSetupStartPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: peer}
	recorder := httptest.NewRecorder()
	server.handleSpeechSetupStart(recorder, request)
	return recorder
}
