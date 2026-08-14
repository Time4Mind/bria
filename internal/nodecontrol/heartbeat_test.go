package nodecontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

type heartbeatRecorder struct {
	calls  int
	report Heartbeat
}

func (r *heartbeatRecorder) CommitHeartbeat(
	_ context.Context,
	report Heartbeat,
) (HeartbeatAck, error) {
	r.calls++
	r.report = report
	return HeartbeatAck{}, nil
}

func TestHeartbeatRejectsCertificatePayloadIdentityMismatch(t *testing.T) {
	server, followerCertificate, recorder := heartbeatTestServer(t, "leader")
	report := Heartbeat{ReportID: "report", NodeID: "leader", BootID: "boot"}
	request := heartbeatRequest(t, report, followerCertificate)
	response := httptest.NewRecorder()
	server.handleHeartbeat(response, request)
	if response.Code != http.StatusForbidden || recorder.calls != 0 {
		t.Fatalf("status=%d commits=%d", response.Code, recorder.calls)
	}
}

func TestHeartbeatRejectsServerThatIsNoLongerLeader(t *testing.T) {
	server, followerCertificate, recorder := heartbeatTestServer(t, "replacement")
	report := Heartbeat{ReportID: "report", NodeID: "target", BootID: "boot"}
	request := heartbeatRequest(t, report, followerCertificate)
	response := httptest.NewRecorder()

	server.handleHeartbeat(response, request)

	if response.Code != http.StatusConflict || recorder.calls != 0 {
		t.Fatalf("status=%d commits=%d", response.Code, recorder.calls)
	}
}

func TestHeartbeatRejectsQuotaForAnotherNode(t *testing.T) {
	server, followerCertificate, recorder := heartbeatTestServer(t, "leader")
	report := Heartbeat{
		ReportID: "report", NodeID: "target", BootID: "boot",
		Quotas: []domain.QuotaSnapshot{{
			NodeID: "other", Backend: "codex", CollectedAt: time.Now(),
		}},
	}
	request := heartbeatRequest(t, report, followerCertificate)
	response := httptest.NewRecorder()
	server.handleHeartbeat(response, request)
	if response.Code != http.StatusBadRequest || recorder.calls != 0 {
		t.Fatalf("status=%d commits=%d", response.Code, recorder.calls)
	}
}

func TestHeartbeatRejectsFalseBackendIsolationClaim(t *testing.T) {
	server, followerCertificate, recorder := heartbeatTestServer(t, "leader")
	report := Heartbeat{
		ReportID: "report", NodeID: "target", BootID: "boot",
		BackendIsolation: domain.BackendIsolationReport{Mode: "trusted", Ready: true},
	}
	request := heartbeatRequest(t, report, followerCertificate)
	response := httptest.NewRecorder()
	server.handleHeartbeat(response, request)
	if response.Code != http.StatusBadRequest || recorder.calls != 0 {
		t.Fatalf("status=%d commits=%d", response.Code, recorder.calls)
	}
}

func TestHeartbeatUsesAuthenticatedCertificateFingerprints(t *testing.T) {
	server, followerCertificate, recorder := heartbeatTestServer(t, "leader")
	report := Heartbeat{
		ReportID: "report", NodeID: "target", BootID: "boot",
		CertificateFingerprint:         strings.Repeat("f", 64),
		PreviousCertificateFingerprint: strings.Repeat("e", 64),
	}
	request := heartbeatRequest(t, report, followerCertificate)
	response := httptest.NewRecorder()
	server.handleHeartbeat(response, request)
	if response.Code != http.StatusOK || recorder.calls != 1 {
		t.Fatalf("status=%d commits=%d", response.Code, recorder.calls)
	}
	want, err := security.NodeCertificateFingerprint(followerCertificate.PeerCertificates[0])
	if err != nil {
		t.Fatal(err)
	}
	if recorder.report.CertificateFingerprint != want ||
		recorder.report.PreviousCertificateFingerprint != "" {
		t.Fatalf("committed certificate evidence=%#v", recorder.report)
	}
}

func TestHeartbeatAgentFollowsLeaderChanges(t *testing.T) {
	leaders := &mutableLeadership{id: "first"}
	publisher := &heartbeatPublisherRecorder{}
	agent, err := NewHeartbeatAgent(
		leaders,
		publisher,
		func(context.Context) (Heartbeat, error) {
			return Heartbeat{NodeID: "follower", BootID: "boot", OS: "linux", Arch: "arm64"}, nil
		},
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	agent.newID = func() (string, error) { return "report", nil }
	if _, err := agent.PublishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	leaders.set("second")
	if _, err := agent.PublishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := publisher.leaderIDs; len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("published leaders=%v", got)
	}
}

func TestOfflineMonitorRunsOnlyOnCurrentLeader(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	seenAt := time.Unix(10, 0).UTC()
	if result := applyTestCommand(t, machine, "add", clusterstate.CommandAddNode, time.Unix(1, 0), domain.Node{
		ID: "node", Name: "Node", Status: domain.NodeOnline, LastSeenAt: seenAt,
	}); result.Err() != nil {
		t.Fatal(result.Err())
	}
	leaders := &mutableLocalLeadership{}
	monitor, err := NewOfflineMonitor(leaders, machine, machineApplier{machine}, 5*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	monitor.now = func() time.Time { return time.Unix(20, 0).UTC() }
	if err := monitor.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := machine.State().Nodes["node"].Status; status != domain.NodeOnline {
		t.Fatalf("follower changed status to %q", status)
	}
	leaders.leader = true
	if err := monitor.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	node := machine.State().Nodes["node"]
	if node.Status != domain.NodeOffline || !node.LastSeenAt.Equal(seenAt) {
		t.Fatalf("offline node=%#v", node)
	}
}

func TestRemoteRecoveryApplierAllowsOnlyOwnedRecoveryTransitions(t *testing.T) {
	leaders := &mutableLeadership{id: "leader"}
	reporter := &recoveryReporterRecorder{}
	applier, err := NewRemoteRecoveryApplier("target", leaders, reporter)
	if err != nil {
		t.Fatal(err)
	}
	valid := recoveryCommand(t, clusterstate.CommandCompleteBootRecovery, domain.SessionRef{
		NodeID: "target", SessionID: "session",
	})
	if _, err := applier.Apply(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	if reporter.report.NodeID != "target" || reporter.report.Outcome != RecoveryComplete ||
		reporter.leaderID != "leader" {
		t.Fatalf("recovery report=%#v leader=%q", reporter.report, reporter.leaderID)
	}
	missing := recoveryCommand(t, clusterstate.CommandMarkMissing, domain.SessionRef{
		NodeID: "target", SessionID: "session",
	})
	missing.Payload, _ = json.Marshal(clusterstate.MarkMissing{
		Session:   domain.SessionRef{NodeID: "target", SessionID: "session"},
		ArchiveID: "missing-session",
	})
	if _, err := applier.Apply(context.Background(), missing); err != nil {
		t.Fatal(err)
	}
	if reporter.report.Outcome != RecoveryMissing {
		t.Fatalf("missing runtime outcome=%q", reporter.report.Outcome)
	}
	foreign := recoveryCommand(t, clusterstate.CommandFailBootRecovery, domain.SessionRef{
		NodeID: "other", SessionID: "session",
	})
	if _, err := applier.Apply(context.Background(), foreign); err != domain.ErrAccessDenied {
		t.Fatalf("foreign recovery error=%v", err)
	}
	unsupported := valid
	unsupported.Kind = clusterstate.CommandAddNode
	if _, err := applier.Apply(context.Background(), unsupported); err == nil {
		t.Fatal("non-recovery command was accepted")
	}
}

func heartbeatTestServer(
	t *testing.T,
	leaderID string,
) (*Server, *tls.ConnectionState, *heartbeatRecorder) {
	t.Helper()
	state := controlState(t)
	guard, err := NewStateGuard(staticState{state})
	if err != nil {
		t.Fatal(err)
	}
	ca, _, _, err := security.GenerateCA("cluster", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	follower := issueTLSCertificate(t, ca, "cluster", "target")
	recorder := &heartbeatRecorder{}
	return &Server{
		nodeID: "leader", clusterID: "cluster", leadership: staticLeadership(leaderID),
		membership: guard, heartbeats: recorder,
	}, &tls.ConnectionState{PeerCertificates: follower.LeafCertificate}, recorder
}

func heartbeatRequest(
	t *testing.T,
	report Heartbeat,
	connection *tls.ConnectionState,
) *http.Request {
	t.Helper()
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, heartbeatPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = connection
	return request
}

type mutableLeadership struct {
	mu sync.RWMutex
	id string
}

func (l *mutableLeadership) LeaderID() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.id
}

func (l *mutableLeadership) set(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.id = id
}

type heartbeatPublisherRecorder struct{ leaderIDs []string }

func (r *heartbeatPublisherRecorder) PublishHeartbeat(
	_ context.Context,
	leaderID string,
	_ Heartbeat,
) (HeartbeatAck, error) {
	r.leaderIDs = append(r.leaderIDs, leaderID)
	return HeartbeatAck{}, nil
}

type mutableLocalLeadership struct{ leader bool }

func (l *mutableLocalLeadership) IsLeader() bool { return l.leader }

type recoveryReporterRecorder struct {
	leaderID string
	report   RecoveryReport
}

func (r *recoveryReporterRecorder) ReportRecovery(
	_ context.Context,
	leaderID string,
	report RecoveryReport,
) error {
	r.leaderID = leaderID
	r.report = report
	return nil
}

type machineApplier struct{ machine *clusterstate.Machine }

func (a machineApplier) Apply(
	_ context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	return a.machine.Apply(command), nil
}

func applyTestCommand(
	t *testing.T,
	machine *clusterstate.Machine,
	operationID string,
	kind clusterstate.CommandKind,
	at time.Time,
	payload any,
) clusterstate.Result {
	t.Helper()
	command, err := clusterstate.NewCommand(operationID, kind, at, payload)
	if err != nil {
		t.Fatal(err)
	}
	return machine.Apply(command)
}

func recoveryCommand(
	t *testing.T,
	kind clusterstate.CommandKind,
	ref domain.SessionRef,
) clusterstate.Command {
	t.Helper()
	command, err := clusterstate.NewCommand(
		"recovery-report", kind, time.Now(), clusterstate.BootRecovery{Session: ref},
	)
	if err != nil {
		t.Fatal(err)
	}
	return command
}
