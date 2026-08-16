package nodecontrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

type rejectingRecoveryCommitter struct{}

func (rejectingRecoveryCommitter) CommitRecovery(context.Context, RecoveryReport) error {
	return domain.ErrInvalidState
}

func TestReportRecoveryClassifiesAlreadySettledConflict(t *testing.T) {
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
	caller := issueTLSCertificate(t, ca, "cluster", "leader")
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate,
		Roots: roots, Leadership: healthObserver{leader: "target"}, Membership: guard,
		Service: &submitRecorder{}, Recovery: rejectingRecoveryCommitter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener := newHealthListener(t, server)
	client, err := NewClient(ClientConfig{
		Certificate: caller.Certificate, Roots: roots, ClusterID: "cluster",
		Resolver: NewStaticResolver(map[string]string{"target": listener.Addr().String()}),
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)

	err = client.ReportRecovery(context.Background(), "target", RecoveryReport{
		ReportID: "retry-after-rollback", NodeID: "leader",
		Session: domain.SessionRef{NodeID: "leader", SessionID: "already-archived"},
		Outcome: RecoveryMissing,
	})
	if !errors.Is(err, ErrRecoveryAlreadySettled) {
		t.Fatalf("error=%v, want ErrRecoveryAlreadySettled", err)
	}
}
