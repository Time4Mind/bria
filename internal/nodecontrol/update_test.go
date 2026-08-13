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

	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/security"
)

type updateRecorder struct{ starts int }

func (*updateRecorder) Inspect(context.Context) (clusterupdate.VerifiedManifest, error) {
	return clusterupdate.VerifiedManifest{}, nil
}
func (r *updateRecorder) Start(_ context.Context, request clusterupdate.Request) (clusterupdate.Status, error) {
	r.starts++
	return clusterupdate.Status{NodeID: request.NodeID, UpdateID: request.UpdateID, Phase: clusterupdate.PhaseDownloading}, nil
}
func (*updateRecorder) Status(_ context.Context, request clusterupdate.Request) (clusterupdate.Status, error) {
	return clusterupdate.Status{NodeID: request.NodeID, UpdateID: request.UpdateID, Phase: clusterupdate.PhaseDownloading}, nil
}

func TestUpdateStartAcceptsOnlyCurrentLeaderAndExactTarget(t *testing.T) {
	state := controlState(t)
	guard, _ := NewStateGuard(staticState{state})
	runtimeService, _ := NewService("target", guard, &fakeExecutor{})
	ca, caPEM, _, _ := security.GenerateCA("cluster", time.Now(), time.Hour)
	target := issueTLSCertificate(t, ca, "cluster", "target")
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, _ := security.CertificatePool(caPEM)
	updates := &updateRecorder{}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate, Roots: roots,
		Leadership: staticLeadership("leader"), Membership: guard, Service: runtimeService,
		Updates: updates,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := clusterupdate.Request{
		NodeID: "target", UpdateID: "job", Version: "v2",
		ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	recorder := updateStartRequest(t, server, leader.LeafCertificate, request)
	if recorder.Code != http.StatusOK || updates.starts != 1 {
		t.Fatalf("leader status=%d starts=%d", recorder.Code, updates.starts)
	}
	server.leadership = staticLeadership("target")
	recorder = updateStartRequest(t, server, leader.LeafCertificate, request)
	if recorder.Code != http.StatusConflict || updates.starts != 1 {
		t.Fatalf("stale leader status=%d starts=%d", recorder.Code, updates.starts)
	}
	server.leadership = staticLeadership("leader")
	request.NodeID = "leader"
	recorder = updateStartRequest(t, server, leader.LeafCertificate, request)
	if recorder.Code != http.StatusBadRequest || updates.starts != 1 {
		t.Fatalf("wrong target status=%d starts=%d", recorder.Code, updates.starts)
	}
}

func updateStartRequest(
	t *testing.T, server *Server, peer []*x509.Certificate, input clusterupdate.Request,
) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(input)
	request := httptest.NewRequest(http.MethodPost, updateStartPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: peer}
	recorder := httptest.NewRecorder()
	server.handleUpdateStart(recorder, request)
	return recorder
}
