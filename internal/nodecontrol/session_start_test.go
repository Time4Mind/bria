package nodecontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/security"
	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/transcript"
)

type startsRecorder struct{ browses int }

func (s *startsRecorder) Browse(
	_ context.Context,
	request sessionstart.BrowseRequest,
) (sessionstart.BrowseResult, error) {
	s.browses++
	return sessionstart.BrowseResult{Path: request.Path}, nil
}

func (*startsRecorder) Discover(
	context.Context,
	sessionstart.DiscoverRequest,
) (transcript.Discovery, error) {
	return transcript.Discovery{}, nil
}

func (*startsRecorder) Provision(context.Context, sessionstart.ProvisionRequest) error { return nil }

func TestStartEndpointsAcceptOnlyCurrentLeaderMember(t *testing.T) {
	state := controlState(t)
	guard, err := NewStateGuard(staticState{state})
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{}
	runtimeService, err := NewService("target", guard, executor)
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
	starts := &startsRecorder{}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate, Roots: roots,
		Leadership: staticLeadership("leader"), Membership: guard,
		Service: runtimeService, Starts: starts,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(sessionstart.BrowseRequest{ActorID: 1, NodeID: "target", Path: "/srv"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, startBrowsePath, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: leader.LeafCertificate}
	recorder := httptest.NewRecorder()
	server.handleStartBrowse(recorder, request)
	if recorder.Code != http.StatusOK || starts.browses != 1 {
		t.Fatalf("status=%d browses=%d body=%q", recorder.Code, starts.browses, recorder.Body.String())
	}
	server.leadership = staticLeadership("target")
	recorder = httptest.NewRecorder()
	server.handleStartBrowse(recorder, request)
	if recorder.Code != http.StatusConflict || starts.browses != 1 {
		t.Fatalf("stale leader status=%d browses=%d", recorder.Code, starts.browses)
	}
}
