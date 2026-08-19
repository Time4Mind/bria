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

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/security"
)

type enrollmentRecorder struct{ calls int }

func (r *enrollmentRecorder) CommitEnrollment(context.Context, EnrollmentReport) error {
	r.calls++
	return nil
}

func TestMembershipRelocationPublishesAddressBeforeWaitingForRaft(t *testing.T) {
	state := controlState(t)
	guard, err := NewStateGuard(staticState{state})
	if err != nil {
		t.Fatal(err)
	}
	ca, caPEM, _, err := security.GenerateCA("cluster", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	admin := &membershipAdminRecorder{
		nodeID: "leader", leader: "leader", members: map[string]bool{"target": true},
		addresses: make(map[string]string),
	}
	service, err := NewService("leader", guard, &fakeExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{
		NodeID: "leader", ClusterID: "cluster", Certificate: leader.Certificate, Roots: roots,
		Leadership: staticLeadership("leader"), Membership: guard, Service: service, Admin: admin,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(MembershipRelocation{
		NodeID: "target", RaftAddress: "target.bria.internal:7946",
		ControlAddress: "target.bria.internal:7947",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, membershipMovePath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: leader.LeafCertificate}
	recorder := httptest.NewRecorder()
	server.handleMembershipRelocation(recorder, request)
	if recorder.Code != http.StatusNoContent ||
		admin.command.Kind != clusterstate.CommandUpdateNodeMetadata ||
		!admin.IsMemberAt("target", "target.bria.internal:7946") {
		t.Fatalf("status=%d kind=%q address=%q", recorder.Code, admin.command.Kind,
			admin.addresses["target"])
	}
}

func TestEnrollmentForwardingAcceptsOnlyConfiguredIssuer(t *testing.T) {
	state := controlState(t)
	for _, nodeID := range []domain.NodeID{"issuer", "attacker"} {
		if err := state.AddNode(domain.Node{ID: nodeID, Name: string(nodeID)}); err != nil {
			t.Fatal(err)
		}
	}
	guard, _ := NewStateGuard(staticState{state})
	executor := &fakeExecutor{}
	service, _ := NewService("target", guard, executor)
	ca, caPEM, _, _ := security.GenerateCA("cluster", time.Now(), time.Hour)
	target := issueTLSCertificate(t, ca, "cluster", "target")
	issuer := issueTLSCertificate(t, ca, "cluster", "issuer")
	attacker := issueTLSCertificate(t, ca, "cluster", "attacker")
	roots, _ := security.CertificatePool(caPEM)
	enrollments := &enrollmentRecorder{}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate, Roots: roots,
		Leadership: staticLeadership("target"), Membership: guard, Service: service,
		Enrollments: enrollments, EnrollmentIssuerID: "issuer",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		peer issuedCertificate
		want int
	}{{"other member", attacker, http.StatusForbidden}, {"issuer", issuer, http.StatusNoContent}} {
		t.Run(test.name, func(t *testing.T) {
			body, _ := json.Marshal(EnrollmentReport{ReportID: "report", ExpectedHash: "hash"})
			request := httptest.NewRequest(http.MethodPost, enrollmentReportPath, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.TLS = &tls.ConnectionState{PeerCertificates: test.peer.LeafCertificate}
			recorder := httptest.NewRecorder()
			server.handleEnrollment(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status=%d, want %d", recorder.Code, test.want)
			}
		})
	}
	if enrollments.calls != 1 {
		t.Fatalf("enrollment commits=%d", enrollments.calls)
	}
}

type issuedCertificate struct {
	tls.Certificate
	LeafCertificate []*x509.Certificate
}

func issueTLSCertificate(
	t *testing.T,
	ca security.CertificateAuthority,
	clusterID string,
	nodeID string,
) issuedCertificate {
	t.Helper()
	credentials, err := security.IssueNodeCertificate(ca, clusterID, nodeID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(credentials.CertificatePEM, credentials.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := security.ParseCertificate(credentials.CertificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	// Keep a transport-ready certificate and a convenient request TLS view.
	return issuedCertificate{Certificate: certificate, LeafCertificate: []*x509.Certificate{leaf}}
}

func controlState(t *testing.T) *domain.State {
	t.Helper()
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "leader", Name: "Leader", Status: domain.NodeOnline},
		{ID: "target", Name: "Target", Status: domain.NodeOnline},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "leader", "target"); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(2, domain.RoleMember, "target"); err != nil {
		t.Fatal(err)
	}
	if err := state.AddSession(domain.Session{
		ID: "s", NodeID: "target", OwnerID: 1, Name: "Session", Backend: "claude",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 1,
		CreatedAt: time.Now(), LiveSinceAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func runtimeRequest(action runtimehost.Action) runtimehost.Request {
	request := runtimehost.Request{
		OperationID: "op", ActorID: 1, NodeID: "target", SessionID: "s",
		ExpectedGeneration: 1, Action: action, Backend: "claude",
	}
	if action == runtimehost.ActionClose {
		request.ArchiveCommitID = "archive"
	}
	return request
}
