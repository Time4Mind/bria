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

type staticState struct{ state *domain.State }

func (s staticState) State() *domain.State { return s.state.Clone() }

type fakeExecutor struct {
	requests []runtimehost.Request
}

func (e *fakeExecutor) Submit(
	_ context.Context,
	request runtimehost.Request,
) (runtimehost.Receipt, error) {
	e.requests = append(e.requests, request)
	return runtimehost.Receipt{
		OperationID: request.OperationID, Accepted: true, Detail: "operation queued",
	}, nil
}

func (*fakeExecutor) LookupResult(context.Context, string) (runtimehost.Result, bool, error) {
	return runtimehost.Result{}, false, nil
}

type staticLeadership string

func (l staticLeadership) LeaderID() string { return string(l) }

type membershipAdminRecorder struct {
	nodeID    string
	leader    string
	members   map[string]bool
	addresses map[string]string
	command   clusterstate.Command
}

func (a *membershipAdminRecorder) Apply(
	_ context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	a.command = command
	if command.Kind == clusterstate.CommandUpdateNodeMetadata {
		var node domain.Node
		if err := json.Unmarshal(command.Payload, &node); err != nil {
			return clusterstate.Result{}, err
		}
		a.addresses[string(node.ID)] = node.Network.RaftAddress
	} else {
		target, remove, err := retirementTarget(command)
		if err != nil {
			return clusterstate.Result{}, err
		}
		if remove {
			delete(a.members, target)
		}
	}
	return clusterstate.Result{}, nil
}

func (a *membershipAdminRecorder) LeaderID() string { return a.leader }
func (a *membershipAdminRecorder) IsMember(nodeID string) bool {
	return a.members[nodeID]
}
func (a *membershipAdminRecorder) IsMemberAt(nodeID, address string) bool {
	return a.members[nodeID] && a.addresses[nodeID] == address
}

type submitRecorder struct{ calls int }

type enrollmentRecorder struct{ calls int }

func (r *enrollmentRecorder) CommitEnrollment(context.Context, EnrollmentReport) error {
	r.calls++
	return nil
}

func (r *submitRecorder) Submit(
	_ context.Context,
	request runtimehost.Request,
) (runtimehost.Receipt, error) {
	r.calls++
	return runtimehost.Receipt{OperationID: request.OperationID, Accepted: true}, nil
}

func (*submitRecorder) LookupResult(
	context.Context,
	runtimehost.Request,
) (runtimehost.Result, bool, error) {
	return runtimehost.Result{}, false, nil
}

func TestStateGuardPinsCertificateAndRejectsRevokedOrDisabledIdentity(t *testing.T) {
	now := time.Now()
	ca, _, _, err := security.GenerateCA("cluster", now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	current := issueTLSCertificate(t, ca, "cluster", "leader")
	replacement := issueTLSCertificate(t, ca, "cluster", "leader")
	fingerprint, err := security.NodeCertificateFingerprint(current.LeafCertificate[0])
	if err != nil {
		t.Fatal(err)
	}
	state := controlState(t)
	node := state.Nodes["leader"]
	node.Fingerprint = fingerprint
	state.Nodes["leader"] = node
	guard, _ := NewStateGuard(staticState{state})
	if !guard.AuthorizeCertificate("leader", current.LeafCertificate[0]) {
		t.Fatal("active certificate rejected")
	}
	if guard.AuthorizeCertificate("leader", replacement.LeafCertificate[0]) {
		t.Fatal("CA-valid replacement without rotation proof accepted")
	}
	node.Lifecycle = domain.NodeDisabled
	state.Nodes["leader"] = node
	if guard.AuthorizeCertificate("leader", current.LeafCertificate[0]) {
		t.Fatal("disabled node certificate accepted")
	}
}

func TestRouterUsesOnlyOwningNodePath(t *testing.T) {
	local := &submitRecorder{}
	remote := &submitRecorder{}
	router, err := NewRouter("local", local, remote)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeRequest(runtimehost.ActionStop)
	request.NodeID = "local"
	if _, err := router.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.NodeID = "remote"
	if _, err := router.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if local.calls != 1 || remote.calls != 1 {
		t.Fatalf("local/remote calls=%d/%d", local.calls, remote.calls)
	}
}

func TestServerAcceptsOnlyCurrentLeaderCertificate(t *testing.T) {
	state := controlState(t)
	guard, err := NewStateGuard(staticState{state})
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{}
	service, err := NewService("target", guard, executor)
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
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate, Roots: roots,
		Leadership: staticLeadership("leader"), Membership: guard, Service: service,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(runtimeRequest(runtimehost.ActionStop))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, executePath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: leader.LeafCertificate}
	recorder := httptest.NewRecorder()
	server.handleExecute(recorder, request)
	if recorder.Code != http.StatusAccepted || len(executor.requests) != 1 {
		t.Fatalf("status=%d requests=%d body=%q", recorder.Code, len(executor.requests), recorder.Body.String())
	}

	server.leadership = staticLeadership("target")
	recorder = httptest.NewRecorder()
	server.handleExecute(recorder, request)
	if recorder.Code != http.StatusConflict || len(executor.requests) != 1 {
		t.Fatalf("stale leader status=%d requests=%d", recorder.Code, len(executor.requests))
	}
}

func TestMembershipAdminAcceptsOnlyLocalLeaderRetirement(t *testing.T) {
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
	target := issueTLSCertificate(t, ca, "cluster", "target")
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	admin := &membershipAdminRecorder{
		nodeID: "leader", leader: "leader", members: map[string]bool{"target": true},
		addresses: make(map[string]string),
	}
	executor := &fakeExecutor{}
	service, err := NewService("leader", guard, executor)
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
	command, err := clusterstate.NewCommand(
		"disable-target", clusterstate.CommandSetNodeLifecycle, time.Now(),
		clusterstate.SetNodeLifecycle{NodeID: "target", Lifecycle: domain.NodeDisabled},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, membershipAdminPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: leader.LeafCertificate}
	recorder := httptest.NewRecorder()
	server.handleMembershipAdmin(recorder, request)
	if recorder.Code != http.StatusOK || admin.command.Kind != clusterstate.CommandSetNodeLifecycle ||
		admin.IsMember("target") {
		t.Fatalf("status=%d kind=%q target_member=%t", recorder.Code, admin.command.Kind,
			admin.IsMember("target"))
	}

	request = httptest.NewRequest(http.MethodPost, membershipAdminPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: target.LeafCertificate}
	recorder = httptest.NewRecorder()
	server.handleMembershipAdmin(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("remote member status=%d", recorder.Code)
	}
}

func TestRetirementTargetRejectsEnableAndLocalDeleteIsCheckedByHandler(t *testing.T) {
	enable, err := clusterstate.NewCommand(
		"enable", clusterstate.CommandSetNodeLifecycle, time.Now(),
		clusterstate.SetNodeLifecycle{NodeID: "target", Lifecycle: domain.NodeActive},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := retirementTarget(enable); err == nil {
		t.Fatal("enable command accepted")
	}
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
