package consensus

import (
	"fmt"

	"github.com/hashicorp/raft"
)

func (n *Node) TransferLeadership(id, address string) error {
	return n.raft.LeadershipTransferToServer(
		raft.ServerID(id),
		raft.ServerAddress(address),
	).Error()
}

func (n *Node) TransferLeadershipTo(id string) error {
	configuration := n.raft.GetConfiguration()
	if err := configuration.Error(); err != nil {
		return err
	}
	for _, server := range configuration.Configuration().Servers {
		if string(server.ID) == id && server.Suffrage == raft.Voter {
			return n.TransferLeadership(id, string(server.Address))
		}
	}
	return fmt.Errorf("voting server %q not found", id)
}
