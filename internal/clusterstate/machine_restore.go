package clusterstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/Time4Mind/bria/internal/domain"
)

func (m *Machine) applyClusterRestore(command Command, result Result) Result {
	var payload RestoreCluster
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		result.Error = fmt.Errorf("decode restore_cluster: %w", err).Error()
		m.remember(result)
		return result
	}
	digest, digestErr := hex.DecodeString(payload.BackupSHA256)
	snapshotDigest := sha256.Sum256(payload.Snapshot)
	if !reflect.DeepEqual(m.state, domain.NewState()) || len(m.ledger) != 0 ||
		digestErr != nil || len(digest) != 32 || hex.EncodeToString(digest) != payload.BackupSHA256 {
		result.Error = fmt.Errorf(
			"%w: cluster restore requires empty compatible state", domain.ErrInvalidState,
		).Error()
		m.remember(result)
		return result
	}
	if !reflect.DeepEqual(digest, snapshotDigest[:]) {
		result.Error = fmt.Errorf("%w: restore snapshot checksum mismatch", domain.ErrInvalidState).Error()
		m.remember(result)
		return result
	}
	restored, err := decodeSnapshot(payload.Snapshot)
	if err != nil {
		result.Error = fmt.Errorf("decode restore snapshot: %w", err).Error()
		m.remember(result)
		return result
	}
	if _, collision := restored.Ledger[command.OperationID]; collision {
		result.Error = fmt.Errorf("%w: restore operation collides with backup ledger", domain.ErrInvalidState).Error()
		m.remember(result)
		return result
	}
	m.installSnapshot(restored)
	m.remember(result)
	return result
}
