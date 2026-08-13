package nodecontrol

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"time"

	"github.com/Time4Mind/bria/internal/clusterbackup"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

const backupPath = "/v1/cluster/backup"

type BackupLeaderHint struct {
	NodeID         string `json:"node_id"`
	ControlAddress string `json:"control_address"`
	Fingerprint    string `json:"fingerprint"`
}

func (s *Server) handleBackup(writer http.ResponseWriter, request *http.Request) {
	if _, ok := s.authorizeHealthMember(writer, request); !ok {
		return
	}
	if s.backups == nil {
		http.Error(writer, "backup service unavailable", http.StatusServiceUnavailable)
		return
	}
	snapshot, err := s.backups.MarshalSnapshot()
	if err != nil {
		http.Error(writer, "snapshot unavailable", http.StatusServiceUnavailable)
		return
	}
	if leaderID := s.leadership.LeaderID(); leaderID == "" || leaderID != s.nodeID {
		s.writeBackupLeaderHint(writer, snapshot, leaderID)
		return
	}
	envelope, err := clusterbackup.New(s.clusterID, s.nodeID, snapshot, time.Now())
	if err != nil {
		http.Error(writer, "snapshot cannot be backed up", http.StatusServiceUnavailable)
		return
	}
	privateKey, ok := s.backupCertificate.PrivateKey.(ed25519.PrivateKey)
	if !ok || len(s.backupCertificate.Certificate) == 0 {
		http.Error(writer, "backup signing identity unavailable", http.StatusServiceUnavailable)
		return
	}
	leaf, err := x509.ParseCertificate(s.backupCertificate.Certificate[0])
	if err != nil || !backupSnapshotMatchesIdentity(snapshot, s.nodeID, leaf) {
		http.Error(writer, "backup identity is not committed", http.StatusConflict)
		return
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: s.backupCertificate.Certificate[0],
	})
	if err := envelope.Sign(certificatePEM, privateKey); err != nil {
		http.Error(writer, "backup cannot be signed", http.StatusServiceUnavailable)
		return
	}
	encoded, err := envelope.Marshal()
	if err != nil {
		http.Error(writer, "backup cannot be encoded", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}

func (s *Server) writeBackupLeaderHint(writer http.ResponseWriter, snapshot []byte, leaderID string) {
	state, err := clusterstate.InspectSnapshot(snapshot)
	if err != nil {
		http.Error(writer, "current backup leader unavailable", http.StatusConflict)
		return
	}
	leader, ok := state.Nodes[domain.NodeID(leaderID)]
	if !ok || !leader.Enabled() || leader.Network.ControlAddress == "" {
		http.Error(writer, "current backup leader unavailable", http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(writer).Encode(BackupLeaderHint{
		NodeID: leaderID, ControlAddress: leader.Network.ControlAddress,
		Fingerprint: leader.Fingerprint,
	})
}

func backupSnapshotMatchesIdentity(
	snapshot []byte,
	nodeID string,
	certificate *x509.Certificate,
) bool {
	state, err := clusterstate.InspectSnapshot(snapshot)
	if err != nil {
		return false
	}
	fingerprint, err := security.NodeCertificateFingerprint(certificate)
	if err != nil {
		return false
	}
	node, ok := state.Nodes[domain.NodeID(nodeID)]
	return ok && node.Enabled() && node.Fingerprint == fingerprint
}
