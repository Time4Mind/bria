package clusterstate_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func command(t *testing.T, id string, kind clusterstate.CommandKind, payload any) clusterstate.Command {
	t.Helper()
	result, err := clusterstate.NewCommand(id, kind, time.Unix(100, 0), payload)
	if err != nil {
		t.Fatalf("new command: %v", err)
	}
	return result
}

func TestRollingVersionBoundaryRejectsUnknownCommandWithoutMutatingState(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	baseline := command(t, "baseline", clusterstate.CommandAddNode,
		domain.Node{ID: "alpha", Name: "Alpha"})
	if result := machine.Apply(baseline); result.Err() != nil {
		t.Fatal(result.Err())
	}
	future := command(t, "future", clusterstate.CommandAddNode,
		domain.Node{ID: "beta", Name: "Beta"})
	future.Version = clusterstate.CommandVersion + 1
	first := machine.Apply(future)
	if first.Err() == nil || !strings.Contains(first.Error, "unsupported command version") {
		t.Fatalf("future result=%#v", first)
	}
	if _, exists := machine.State().Nodes["beta"]; exists {
		t.Fatal("future command partially mutated old state machine")
	}
	if duplicate := machine.Apply(future); duplicate.Error != first.Error {
		t.Fatalf("future command outcome was not deterministic: %#v / %#v", first, duplicate)
	}
	current := command(t, "current", clusterstate.CommandAddNode,
		domain.Node{ID: "gamma", Name: "Gamma"})
	if result := machine.Apply(current); result.Err() != nil {
		t.Fatalf("current command after future rejection: %v", result.Err())
	}
}

func TestRollingVersionBoundaryRejectsFutureSnapshotAndKeepsCurrentState(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	add := command(t, "alpha", clusterstate.CommandAddNode,
		domain.Node{ID: "alpha", Name: "Alpha"})
	if result := machine.Apply(add); result.Err() != nil {
		t.Fatal(result.Err())
	}
	data, err := machine.MarshalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["version"] = float64(2)
	future, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.RestoreSnapshot(future); err == nil {
		t.Fatal("future snapshot was accepted")
	}
	if got := machine.State().Nodes["alpha"].Name; got != "Alpha" {
		t.Fatalf("failed restore replaced current state: %q", got)
	}
}

func TestMachineAppliesCommandsTransactionallyAndDeduplicates(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	add := command(t, "op-add", clusterstate.CommandAddNode, domain.Node{ID: "alpha", Name: "Alpha"})
	if result := machine.Apply(add); result.Err() != nil {
		t.Fatalf("add node: %v", result.Err())
	}
	duplicate := machine.Apply(add)
	if duplicate.Err() != nil {
		t.Fatalf("duplicate add returned a different outcome: %v", duplicate.Err())
	}
	if got := len(machine.State().Nodes); got != 1 {
		t.Fatalf("node count = %d, want 1", got)
	}

	bad := command(t, "op-bad", clusterstate.CommandAddNode, domain.Node{ID: "alpha", Name: "Other"})
	if result := machine.Apply(bad); result.Err() == nil {
		t.Fatal("duplicate node command unexpectedly succeeded")
	}
	if got := machine.State().Nodes["alpha"].Name; got != "Alpha" {
		t.Fatalf("failed command mutated state: %q", got)
	}
	if result := machine.Apply(bad); result.Err() == nil {
		t.Fatal("failed operation was not deduplicated")
	}
}

func TestMachineSnapshotPreservesStateAndOperationLedger(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	add := command(t, "op-add", clusterstate.CommandAddNode, domain.Node{ID: "alpha", Name: "Alpha"})
	if result := machine.Apply(add); result.Err() != nil {
		t.Fatalf("add node: %v", result.Err())
	}
	data, err := machine.MarshalSnapshot()
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	restored := clusterstate.NewMachine(nil)
	if err := restored.RestoreSnapshot(data); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	if got := restored.State().Nodes["alpha"].Name; got != "Alpha" {
		t.Fatalf("restored node name = %q", got)
	}
	if result := restored.Apply(add); result.Err() != nil {
		t.Fatalf("restored ledger did not deduplicate: %v", result.Err())
	}
}

func TestObserveBootResultIsReplicatedValue(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Alpha", BootID: "old"}); err != nil {
		t.Fatalf("add node: %v", err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "alpha"); err != nil {
		t.Fatalf("node access: %v", err)
	}
	if err := state.AddSession(domain.Session{
		ID: "s1", NodeID: "alpha", OwnerID: 1, Name: "s1", Backend: "codex",
		State: domain.SessionActive, ProviderSessionID: "provider-s1", CreatedAt: time.Unix(10, 0), LiveSinceAt: time.Unix(10, 0), LastEventAt: time.Unix(10, 0),
	}); err != nil {
		t.Fatalf("add session: %v", err)
	}
	machine := clusterstate.NewMachine(state)
	result := machine.Apply(command(t, "op-boot", clusterstate.CommandObserveBoot, clusterstate.ObserveBoot{NodeID: "alpha", BootID: "new"}))
	if result.Err() != nil {
		t.Fatalf("observe boot: %v", result.Err())
	}
	var value domain.BootRecoveryPlan
	if err := json.Unmarshal(result.Value, &value); err != nil {
		t.Fatalf("decode value: %v", err)
	}
	if len(value.Recover) != 1 || len(value.Archived) != 0 {
		t.Fatalf("recovery plan = %#v", value)
	}
}

func TestTelegramCursorIsMonotonicAndSnapshotted(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	advance := command(t, "tg-43", clusterstate.CommandAdvanceTelegramCursor, clusterstate.AdvanceTelegramCursor{NextUpdateID: 43})
	if result := machine.Apply(advance); result.Err() != nil {
		t.Fatal(result.Err())
	}
	backward := command(t, "tg-42", clusterstate.CommandAdvanceTelegramCursor, clusterstate.AdvanceTelegramCursor{NextUpdateID: 42})
	if result := machine.Apply(backward); result.Err() == nil {
		t.Fatal("backward Telegram cursor accepted")
	}
	data, err := machine.MarshalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored := clusterstate.NewMachine(nil)
	if err := restored.RestoreSnapshot(data); err != nil {
		t.Fatal(err)
	}
	if got := restored.State().TelegramNextUpdateID; got != 43 {
		t.Fatalf("restored Telegram cursor=%d", got)
	}
}

func TestTelegramResponseCardIsReplicatedAndSnapshotted(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "alpha"); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	card := domain.TelegramResponseCard{ChatID: 7, MessageID: 91}
	result := machine.Apply(command(t, "tg-card-91", clusterstate.CommandRecordTelegramCard,
		clusterstate.RecordTelegramCard{UserID: 7, Card: card}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	data, err := machine.MarshalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored := clusterstate.NewMachine(nil)
	if err := restored.RestoreSnapshot(data); err != nil {
		t.Fatal(err)
	}
	if got := restored.State().TelegramResponseCards[7]; got != card {
		t.Fatalf("restored response card=%#v", got)
	}
}

func TestLanguagePreferenceIsReplicatedAndSnapshotted(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "alpha"); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	preferences := domain.DefaultUserPreferences()
	preferences.Language = domain.LanguageRussian
	result := machine.Apply(command(t, "language-ru", clusterstate.CommandSetPreferences,
		clusterstate.SetPreferences{UserID: 7, Preferences: preferences}))
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	data, err := machine.MarshalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored := clusterstate.NewMachine(nil)
	if err := restored.RestoreSnapshot(data); err != nil {
		t.Fatal(err)
	}
	if got := restored.State().Preferences[7].Language; got != domain.LanguageRussian {
		t.Fatalf("restored language=%q", got)
	}
}

func TestHeartbeatReplicatesNormalizedQuotaAndAddTime(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	add := command(t, "node-add", clusterstate.CommandAddNode, domain.Node{ID: "alpha", Name: "Alpha"})
	if result := machine.Apply(add); result.Err() != nil {
		t.Fatal(result.Err())
	}
	if got := machine.State().Nodes["alpha"].CreatedAt; !got.Equal(add.IssuedAt) {
		t.Fatalf("created_at=%s want=%s", got, add.IssuedAt)
	}
	collected := time.Unix(120, 0).UTC()
	heartbeat := clusterstate.PublishNodeHeartbeat{
		NodeID: "alpha", BootID: "boot-a", OS: "linux", Arch: "arm64",
		Quotas: []domain.QuotaSnapshot{{
			NodeID: "alpha", Backend: "codex", AccountID: "acct",
			Weekly: &domain.QuotaWindow{UsedPercent: 37}, CollectedAt: collected,
		}},
	}
	if result := machine.Apply(command(t, "heartbeat", clusterstate.CommandPublishNodeHeartbeat, heartbeat)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	got := machine.State().Quotas["alpha/codex"]
	if got.AccountID != "acct" || got.Weekly == nil || got.Weekly.UsedPercent != 37 {
		t.Fatalf("quota=%#v", got)
	}
	node := machine.State().Nodes["alpha"]
	if node.OS != "linux" || node.Arch != "arm64" {
		t.Fatalf("heartbeat platform=%s/%s", node.OS, node.Arch)
	}
	data, err := machine.MarshalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored := clusterstate.NewMachine(nil)
	if err := restored.RestoreSnapshot(data); err != nil {
		t.Fatal(err)
	}
	if got := restored.State().Quotas["alpha/codex"].CollectedAt; !got.Equal(collected) {
		t.Fatalf("restored collected_at=%s", got)
	}
}

func TestQuotaRefreshAndTemporaryLeaderAreReplicated(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Alpha", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	refresh := command(t, "refresh", clusterstate.CommandRequestQuotaRefresh, struct{}{})
	if result := machine.Apply(refresh); result.Err() != nil {
		t.Fatal(result.Err())
	}
	pin := clusterstate.SetTemporaryLeader{NodeID: "alpha", Until: time.Unix(100, 0).Add(30 * time.Minute)}
	if result := machine.Apply(command(t, "pin", clusterstate.CommandSetTemporaryLeader, pin)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	got := machine.State()
	if !got.QuotaRefreshRequestedAt.Equal(refresh.IssuedAt) || got.TemporaryLeader.NodeID != "alpha" {
		t.Fatalf("state=%#v/%#v", got.QuotaRefreshRequestedAt, got.TemporaryLeader)
	}
}
