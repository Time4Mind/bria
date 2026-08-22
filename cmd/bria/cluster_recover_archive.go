package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
)

func recoverClusterArchive(arguments []string) error {
	flags := flag.NewFlagSet("cluster recover-archive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "current leader config path")
	sessionID := flags.String("session", "", "archived session ID")
	providerID := flags.String("provider", "", "recovered provider session ID")
	ownerID := flags.Int64("owner", 0, "archived session owner ID")
	revision := flags.Uint64("revision", 0, "exact archived session revision")
	confirmation := flags.String("confirm", "", "repeat NODE/SESSION")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *sessionID == "" ||
		*providerID == "" || *ownerID <= 0 || *revision == 0 {
		return errors.New("usage: bria cluster recover-archive --config PATH --session SESSION --provider PROVIDER --owner USER --revision N --confirm NODE/SESSION")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ref := domain.SessionRef{
		NodeID: domain.NodeID(nodeConfig.NodeID), SessionID: domain.SessionID(*sessionID),
	}
	if err := ref.Validate(); err != nil || *confirmation != ref.Key() {
		return errors.New("confirmation must exactly match the local NODE/SESSION")
	}
	resolver, err := controlResolver(nodeConfig, nil)
	if err != nil {
		return err
	}
	certificate, roots, err := loadNodeTLS(nodeConfig)
	if err != nil {
		return err
	}
	client, err := nodecontrol.NewClient(nodecontrol.ClientConfig{
		Certificate: certificate, Roots: roots, ClusterID: nodeConfig.ClusterID,
		Resolver: resolver, Timeout: 20 * time.Second,
	})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	command, err := clusterstate.NewCommand(
		"recover-archive-"+*sessionID+"-"+strconv.FormatInt(time.Now().UnixNano(), 10),
		clusterstate.CommandRecoverArchivedSession, time.Now(), clusterstate.RecoverArchivedSession{
			ActorID: domain.UserID(*ownerID), Session: ref,
			ExpectedRevision: *revision, ProviderID: *providerID,
		},
	)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.ApplyArchivePurgeCommand(ctx, nodeConfig.NodeID, command); err != nil {
		return err
	}
	fmt.Printf("queued archived session recovery for %s\n", ref.Key())
	return nil
}
