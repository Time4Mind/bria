package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/Time4Mind/bria/internal/config"
)

func setClusterOwner(arguments []string) error {
	flags := flag.NewFlagSet("cluster set-owner", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "bootstrap node config path")
	userID := flags.Int64("user-id", 0, "new Telegram owner user ID")
	confirmation := flags.String("confirm", "", "repeat the new Telegram user ID")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *userID <= 0 {
		return errors.New("config and a positive user-id are required")
	}
	if *confirmation != strconv.FormatInt(*userID, 10) {
		return errors.New("confirmation must exactly match user-id")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if !nodeConfig.Bootstrap {
		return errors.New("owner can only be changed in the bootstrap node config")
	}
	nodeConfig.BootstrapOwnerID = *userID
	if err := writeConfigAtomic(*configPath, nodeConfig); err != nil {
		return err
	}
	fmt.Printf("owner set to %d; restart the bootstrap node to commit the transfer\n", *userID)
	return nil
}
