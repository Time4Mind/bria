package application

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func stableCommandIdentity(payload any, encoded []byte) ([]byte, error) {
	identity := payload
	switch value := payload.(type) {
	case domain.Session:
		identity = struct {
			ID                domain.SessionID `json:"id"`
			NodeID            domain.NodeID    `json:"node_id"`
			OwnerID           domain.UserID    `json:"owner_id"`
			Workdir           string           `json:"workdir"`
			Backend           string           `json:"backend"`
			ProviderSessionID string           `json:"provider_session_id,omitempty"`
			ProviderResume    bool             `json:"provider_resume,omitempty"`
		}{value.ID, value.NodeID, value.OwnerID, value.Workdir, value.Backend,
			value.ProviderSessionID, value.ProviderResume}
	case clusterstate.SetTemporaryLeader:
		identity = struct {
			NodeID domain.NodeID `json:"node_id"`
		}{value.NodeID}
	case domain.EnrollmentRequest:
		value.RequestedAt = time.Time{}
		value.DecidedAt = time.Time{}
		value.NotifiedAt = time.Time{}
		identity = value
	default:
		return encoded, nil
	}
	result, err := json.Marshal(identity)
	if err != nil {
		return nil, fmt.Errorf("encode stable command identity: %w", err)
	}
	return result, nil
}
