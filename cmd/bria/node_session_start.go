package main

import (
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/transcript"
	"github.com/Time4Mind/bria/internal/workspace"
)

func newLocalSessionStart(
	node *consensus.Node,
	nodeConfig config.Config,
	home string,
	reader *transcript.Reader,
	executor *runtimehost.LocalExecutor,
	client *nodecontrol.Client,
	runner runtimehost.CommandRunner,
) (*sessionstart.Local, *nodecontrol.StartRouter, error) {
	browser, err := workspace.NewBrowser(home)
	if err != nil {
		return nil, nil, err
	}
	runtime, err := runtimehost.NewTmuxRecoveryRuntime(
		runner, nodeConfig.TmuxSession,
		map[string]runtimehost.BackendCommand{
			"claude": {Executable: nodeConfig.ClaudeCommand, Flags: nodeConfig.EffectiveClaudeFlags()},
			"codex":  {Executable: nodeConfig.CodexCommand, Flags: nodeConfig.EffectiveCodexFlags()},
		}, 30*time.Second,
	)
	if err != nil {
		return nil, nil, err
	}
	local, err := sessionstart.NewLocal(
		domain.NodeID(nodeConfig.NodeID), node.State(), browser, reader, runtime, executor,
	)
	if err != nil {
		return nil, nil, err
	}
	router, err := nodecontrol.NewStartRouter(nodeConfig.NodeID, local, client)
	if err != nil {
		return nil, nil, err
	}
	return local, router, nil
}
