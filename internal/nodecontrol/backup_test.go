package nodecontrol

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterbackup"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/security"
)

func TestBackupIsAuthenticatedAndLeaderOnly(t *testing.T) {
	state := controlState(t)
	guard, _ := NewStateGuard(staticState{state})
	ca, _, _, _ := security.GenerateCA("cluster", time.Now(), time.Hour)
	leader := issueTLSCertificate(t, ca, "cluster", "leader")
	fingerprint, err := security.NodeCertificateFingerprint(leader.LeafCertificate[0])
	if err != nil {
		t.Fatal(err)
	}
	leaderNode := state.Nodes["leader"]
	leaderNode.Fingerprint = fingerprint
	leaderNode.Network.ControlAddress = "leader.internal:7443"
	state.Nodes["leader"] = leaderNode
	targetNode := state.Nodes["target"]
	targetNode.Fingerprint = "target-fingerprint"
	targetNode.Network.ControlAddress = "target.internal:7443"
	state.Nodes["target"] = targetNode
	machine := clusterstate.NewMachine(state)
	server := &Server{
		nodeID: "leader", clusterID: "cluster", leadership: staticLeadership("leader"),
		membership: guard, backups: machine,
		backupCertificate: leader.Certificate,
	}
	request := httptest.NewRequest(http.MethodGet, backupPath, nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: leader.LeafCertificate}
	response := httptest.NewRecorder()
	server.handleBackup(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	backup, err := clusterbackup.Parse(response.Body.Bytes())
	if err != nil || backup.ClusterID != "cluster" || backup.SourceNodeID != "leader" {
		t.Fatalf("backup=%#v err=%v", backup, err)
	}
	server.leadership = staticLeadership("target")
	response = httptest.NewRecorder()
	server.handleBackup(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("follower backup status=%d", response.Code)
	}
	var hint BackupLeaderHint
	if err := json.Unmarshal(response.Body.Bytes(), &hint); err != nil ||
		hint.NodeID != "target" {
		t.Fatalf("leader hint=%#v err=%v", hint, err)
	}
	request.TLS = nil
	response = httptest.NewRecorder()
	server.handleBackup(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous backup status=%d", response.Code)
	}
}
