package main

import "errors"

func runCluster(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: bria cluster <init|join|contract|claim|set-peers|issue-node|cert-renew|recover-ca|set-owner|backup|restore|purge-archive|retire-node|relocate-node>")
	}
	switch arguments[0] {
	case "init":
		return initCluster(arguments)
	case "join":
		return joinCluster(arguments[1:])
	case "contract":
		return createNodeContract(arguments[1:])
	case "claim":
		return claimNodeContract(arguments[1:])
	case "set-peers":
		return setClusterPeers(arguments[1:])
	case "issue-node":
		return issueClusterNode(arguments[1:])
	case "cert-renew":
		return renewClusterNodeCertificate(arguments[1:])
	case "recover-ca":
		return recoverClusterCA(arguments[1:])
	case "set-owner":
		return setClusterOwner(arguments[1:])
	case "backup":
		return backupCluster(arguments[1:])
	case "restore":
		return restoreCluster(arguments[1:])
	case "purge-archive":
		return purgeClusterArchive(arguments[1:])
	case "retire-node":
		return retireClusterNode(arguments[1:])
	case "relocate-node":
		return relocateClusterNode(arguments[1:])
	default:
		return errors.New("usage: bria cluster <init|join|contract|claim|set-peers|issue-node|cert-renew|recover-ca|set-owner|backup|restore|purge-archive|retire-node|relocate-node>")
	}
}
