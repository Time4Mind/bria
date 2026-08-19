package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
)

func archiveMissingLocalSessions(
	ctx context.Context,
	node *consensus.Node,
	nodeID string,
	client *nodecontrol.Client,
	refs []domain.SessionRef,
) error {
	if len(refs) == 0 {
		return nil
	}
	remote, err := nodecontrol.NewRemoteRecoveryApplier(nodeID, node, client)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		operationID, operationErr := newOperationID()
		if operationErr != nil {
			return operationErr
		}
		command, commandErr := clusterstate.NewCommand(
			operationID, clusterstate.CommandMarkMissing, time.Now(),
			clusterstate.MarkMissing{
				Session: ref, ArchiveID: clusterstate.MissingArchiveID(operationID),
			},
		)
		if commandErr != nil {
			return commandErr
		}
		var result clusterstate.Result
		if node.IsLeader() {
			result, err = node.Apply(ctx, command)
		} else {
			result, err = remote.Apply(ctx, command)
		}
		if errors.Is(err, nodecontrol.ErrRecoveryAlreadySettled) {
			// The leader has already closed or archived this session while this
			// follower was restarting. Let Raft catch the local projection up;
			// keeping the node offline cannot make that convergence happen.
			continue
		}
		if err != nil {
			return fmt.Errorf("archive missing runtime %s: %w", ref.Key(), err)
		}
		if err := result.Err(); err != nil {
			return fmt.Errorf("archive missing runtime %s: %w", ref.Key(), err)
		}
	}
	return nil
}
