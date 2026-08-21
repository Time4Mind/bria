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
	"github.com/Time4Mind/bria/internal/security"
	"github.com/Time4Mind/bria/internal/sessiondescription"
)

type descriptionRecorder struct {
	requests []sessiondescription.Request
}

func (r *descriptionRecorder) Generate(
	_ context.Context,
	request sessiondescription.Request,
) (sessiondescription.Result, error) {
	r.requests = append(r.requests, request)
	return sessiondescription.Result{Lines: []string{"Context.", "Result."}}, nil
}

func TestArchiveDescriptionEndpointAcceptsOnlyCurrentLeaderAndExactOrigin(t *testing.T) {
	state := controlState(t)
	guard, _ := NewStateGuard(staticState{state})
	executor := &fakeExecutor{}
	runtimeService, _ := NewService("target", guard, executor)
	ca, caPEM, _, _ := security.GenerateCA("cluster", time.Now(), time.Hour)
	target := issueTLSCertificate(t, ca, "cluster", "target")
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, _ := security.CertificatePool(caPEM)
	descriptions := &descriptionRecorder{}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate, Roots: roots,
		Leadership: staticLeadership("leader"), Membership: guard, Service: runtimeService,
		Descriptions: descriptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := sessiondescription.Request{
		NodeID: "target", Session: domain.SessionRef{NodeID: "target", SessionID: "session"},
		ArchiveID: "archive", ExpectedRevision: 4,
	}
	recorder := archiveDescriptionRequest(t, server, leader.LeafCertificate, request)
	if recorder.Code != http.StatusOK || len(descriptions.requests) != 1 {
		t.Fatalf("status=%d requests=%d body=%q", recorder.Code, len(descriptions.requests), recorder.Body.String())
	}
	server.leadership = staticLeadership("target")
	recorder = archiveDescriptionRequest(t, server, leader.LeafCertificate, request)
	if recorder.Code != http.StatusConflict || len(descriptions.requests) != 1 {
		t.Fatalf("stale leader status=%d requests=%d", recorder.Code, len(descriptions.requests))
	}
	server.leadership = staticLeadership("leader")
	request.NodeID = "leader"
	recorder = archiveDescriptionRequest(t, server, leader.LeafCertificate, request)
	if recorder.Code != http.StatusConflict || len(descriptions.requests) != 1 {
		t.Fatalf("wrong origin status=%d requests=%d", recorder.Code, len(descriptions.requests))
	}
}

func archiveDescriptionRequest(
	t *testing.T,
	server *Server,
	peer []*x509.Certificate,
	body sessiondescription.Request,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, archiveDescriptionPath, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: peer}
	recorder := httptest.NewRecorder()
	server.handleArchiveDescription(recorder, request)
	return recorder
}
