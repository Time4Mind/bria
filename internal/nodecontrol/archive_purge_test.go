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

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

type archiveAdminRecorder struct {
	membershipAdminRecorder
}

func (a *archiveAdminRecorder) Apply(
	_ context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	a.command = command
	return clusterstate.Result{OperationID: command.OperationID}, nil
}

func TestArchivePurgeAcceptsOnlyExactLocalLeaderCommand(t *testing.T) {
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
	admin := &archiveAdminRecorder{membershipAdminRecorder: membershipAdminRecorder{
		nodeID: "leader", leader: "leader", members: map[string]bool{"target": true},
		addresses: make(map[string]string),
	}}
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
	command, err := clusterstate.NewCommand(
		"purge-local", clusterstate.CommandPurgeSession, time.Now(),
		clusterstate.PurgeSession{
			Session:   domain.SessionRef{NodeID: "leader", SessionID: "archived"},
			ArchiveID: "archive-local", ExpectedRevision: 3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, archivePurgePath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: leader.LeafCertificate}
	recorder := httptest.NewRecorder()
	server.handleArchivePurge(recorder, request)
	if recorder.Code != http.StatusOK || admin.command.Kind != clusterstate.CommandPurgeSession {
		t.Fatalf("purge status=%d kind=%q", recorder.Code, admin.command.Kind)
	}

	request = httptest.NewRequest(http.MethodPost, archivePurgePath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: target.LeafCertificate}
	recorder = httptest.NewRecorder()
	server.handleArchivePurge(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("remote member status=%d", recorder.Code)
	}
}

func TestArchiveRecoveryAcceptsExactLocalLeaderCommand(t *testing.T) {
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
	admin := &archiveAdminRecorder{membershipAdminRecorder: membershipAdminRecorder{
		nodeID: "leader", leader: "leader", members: map[string]bool{"leader": true},
		addresses: make(map[string]string),
	}}
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
	command, err := clusterstate.NewCommand(
		"recover-local", clusterstate.CommandRecoverArchivedSession, time.Now(),
		clusterstate.RecoverArchivedSession{
			ActorID: 7, Session: domain.SessionRef{NodeID: "leader", SessionID: "archived"},
			ProviderID: "provider", ExpectedRevision: 3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, archivePurgePath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: leader.LeafCertificate}
	recorder := httptest.NewRecorder()
	server.handleArchivePurge(recorder, request)
	if recorder.Code != http.StatusOK ||
		admin.command.Kind != clusterstate.CommandRecoverArchivedSession {
		t.Fatalf("recover status=%d kind=%q", recorder.Code, admin.command.Kind)
	}
}
