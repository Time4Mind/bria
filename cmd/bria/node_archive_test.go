package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/archive"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/localarchive"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

type archiveTranscriptStub struct{}

func (archiveTranscriptStub) Read(context.Context, transcript.Request) ([]transcript.Event, error) {
	return []transcript.Event{{Kind: transcript.EventAssistantFinal, Text: "done"}}, nil
}

type archiveTmuxRunner struct {
	exists bool
	calls  [][]string
}

func (*archiveTmuxRunner) LookPath(string) (string, error) { return "/usr/bin/tmux", nil }

func (r *archiveTmuxRunner) Run(
	_ context.Context,
	_ string,
	arguments ...string,
) (runtimehost.CommandResult, error) {
	r.calls = append(r.calls, append([]string(nil), arguments...))
	if len(arguments) > 0 && arguments[0] == "list-windows" {
		if r.exists {
			return runtimehost.CommandResult{Stdout: []byte(
				runtimehost.TmuxWindowName("node", "session") + "\n",
			)}, nil
		}
		return runtimehost.CommandResult{ExitCode: 1}, nil
	}
	if len(arguments) > 0 && arguments[0] == "kill-window" {
		r.exists = false
	}
	return runtimehost.CommandResult{}, nil
}

func (r *archiveTmuxRunner) RunInput(
	context.Context,
	[]byte,
	string,
	...string,
) (runtimehost.CommandResult, error) {
	return runtimehost.CommandResult{}, nil
}

func TestArchiveReconcilerFinishesCommitAfterCrashWindow(t *testing.T) {
	root := t.TempDir()
	store, err := archive.NewFileStore(filepath.Join(root, "archives"))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := localarchive.NewWriter(store, archiveTranscriptStub{})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0).UTC()
	session := domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Work", Backend: "codex",
		ProviderSessionID: "provider", Workdir: root, State: domain.SessionArchived,
		RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 1,
		CreatedAt: created, LiveSinceAt: created, ArchivedAt: created.Add(time.Minute),
		ArchiveID: "archive-crash", ArchiveReason: domain.ArchiveManual,
	}
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "node", Name: "Node", Status: domain.NodeOnline,
	}); err != nil {
		t.Fatal(err)
	}
	state.Sessions[session.Ref().Key()] = session
	runner := &archiveTmuxRunner{exists: true}
	driver, err := runtimehost.NewTmuxDriver(runner, time.Second, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileLocalArchives(context.Background(), state, config.Config{
		NodeID: "node", TmuxSession: "bria",
	}, driver, writer); err != nil {
		t.Fatal(err)
	}
	ids, err := writer.ReadyArchiveIDs()
	if err != nil || !slices.Equal(ids, []string{"archive-crash"}) {
		t.Fatalf("ready archives=%v err=%v", ids, err)
	}
	if runner.exists {
		t.Fatal("leftover runtime was not deactivated")
	}
	if err := reconcileLocalArchives(context.Background(), state, config.Config{
		NodeID: "node", TmuxSession: "bria",
	}, driver, writer); err != nil {
		t.Fatalf("repeat reconciliation failed: %v", err)
	}
}

func TestArchiveReconcilerPreservesMissingRuntimeReason(t *testing.T) {
	root := t.TempDir()
	store, err := archive.NewFileStore(filepath.Join(root, "archives"))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := localarchive.NewWriter(store, archiveTranscriptStub{})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0).UTC()
	session := domain.Session{
		ID: "missing", NodeID: "node", OwnerID: 7, Name: "Missing", Backend: "codex",
		ProviderSessionID: "provider", Workdir: root, State: domain.SessionArchived,
		RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 2,
		CreatedAt: created, LiveSinceAt: created, ArchivedAt: created.Add(time.Minute),
		ArchiveID: "missing-runtime", ArchiveReason: domain.ArchiveResumeFailed,
	}
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "node", Name: "Node", Status: domain.NodeOnline,
	}); err != nil {
		t.Fatal(err)
	}
	state.Sessions[session.Ref().Key()] = session
	driver, err := runtimehost.NewTmuxDriver(
		&archiveTmuxRunner{}, time.Second, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileLocalArchives(context.Background(), state, config.Config{
		NodeID: "node", TmuxSession: "bria",
	}, driver, writer); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := store.Load(context.Background(), "missing-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Reason != domain.ArchiveResumeFailed {
		t.Fatalf("archive reason=%q", manifest.Reason)
	}
	if err := state.ObserveArchiveInventory(
		"node", []string{"missing-runtime"}, created.Add(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if !state.Sessions[session.Ref().Key()].ArchiveReady {
		t.Fatal("missing runtime native archive did not become ready")
	}
}
