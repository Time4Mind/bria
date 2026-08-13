package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type localMachine struct{ machine *clusterstate.Machine }

func (m localMachine) State() *domain.State { return m.machine.State() }
func (m localMachine) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return clusterstate.Result{}, err
	}
	var decoded clusterstate.Command
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return clusterstate.Result{}, err
	}
	return m.machine.Apply(decoded), nil
}

type serviceLeadership string

func (l serviceLeadership) LeaderID() string { return string(l) }

func TestQueriesFilterNodeACLPrivacyAndViewMode(t *testing.T) {
	state := domain.NewState()
	for _, node := range []domain.Node{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(2, domain.RoleMember, "a"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0)
	for _, session := range []domain.Session{
		{ID: "private", NodeID: "a", OwnerID: 1, Name: "private", State: domain.SessionActive, CreatedAt: created, LiveSinceAt: created},
		{ID: "own", NodeID: "a", OwnerID: 2, Name: "own", State: domain.SessionActive, CreatedAt: created.Add(time.Second), LiveSinceAt: created.Add(time.Second)},
		{ID: "hidden-node", NodeID: "b", OwnerID: 1, Name: "hidden", State: domain.SessionActive, CreatedAt: created, LiveSinceAt: created},
	} {
		if err := state.AddSession(session); err != nil {
			t.Fatal(err)
		}
	}
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := service.ListNodes(application.Principal{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Node.ID != "a" || nodes[0].LiveSessions != 1 {
		t.Fatalf("nodes = %#v", nodes)
	}
	sessions, err := service.ListSessions(application.Principal{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Session.ID != "own" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestClusterAlertTargetUsesSoleOwnerPreferencesAndNodeLifecycle(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "alpha", Name: "Alpha", Lifecycle: domain.NodeDisabled,
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	preferences := state.Preferences[7]
	preferences.Language = domain.LanguageRussian
	state.Preferences[7] = preferences
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := service.ClusterAlertTarget("alpha")
	if !ok || target.OwnerID != 7 || target.NodeName != "Alpha" ||
		target.Language != domain.LanguageRussian || target.Enabled {
		t.Fatalf("target=%#v ok=%t", target, ok)
	}
	if _, ok := service.ClusterAlertTarget("missing"); ok {
		t.Fatal("missing node returned an alert target")
	}
}

func TestSharingIsBlockedAtSingleOwnerApplicationBoundary(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "a"); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(2, domain.RoleMember, "a"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0)
	session := domain.Session{ID: "s", NodeID: "a", OwnerID: 1, Name: "s", State: domain.SessionActive, CreatedAt: created, LiveSinceAt: created}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ShareSession(context.Background(), application.Principal{UserID: 1}, session.Ref(), 2, domain.ShareView); err != domain.ErrAccessDenied {
		t.Fatalf("share error=%v", err)
	}
	if machine.State().CanViewSession(2, session.Ref()) {
		t.Fatal("blocked share mutated replicated state")
	}
}

func TestClusterStatusMutationsRequireOwner(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "a", Name: "A", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "a"); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(2, domain.RoleMember, "a"); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	member := application.Principal{UserID: 2}
	if err := service.RequestQuotaRefresh(context.Background(), member); err != domain.ErrAccessDenied {
		t.Fatalf("member refresh error=%v", err)
	}
	if err := service.SetTemporaryLeader(context.Background(), member, "a"); err != domain.ErrAccessDenied {
		t.Fatalf("member leader error=%v", err)
	}
}

func TestListNodesUsesGlobalLeaderFirstPreference(t *testing.T) {
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "old", Name: "Zulu", CreatedAt: time.Unix(1, 0)},
		{ID: "leader", Name: "Alpha", CreatedAt: time.Unix(2, 0)},
		{ID: "new", Name: "Beta", CreatedAt: time.Unix(3, 0)},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "old", "leader", "new"); err != nil {
		t.Fatal(err)
	}
	preferences := state.Preferences[1]
	preferences.NodeSort = domain.NodeSortLeader
	state.Preferences[1] = preferences
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	service.SetLeadership(serviceLeadership("leader"))
	items, err := service.ListNodes(application.Principal{UserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Node.ID != "leader" ||
		items[1].Node.ID != "old" || items[2].Node.ID != "new" {
		t.Fatalf("nodes=%#v", items)
	}
}

func TestSelectionIsReplicatedAndCannotBypassVisibility(t *testing.T) {
	state := domain.NewState()
	for _, node := range []domain.Node{{ID: "allowed", Name: "Allowed"}, {ID: "hidden", Name: "Hidden"}} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(1, domain.RoleMember, "allowed"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0)
	session := domain.Session{
		ID: "own", NodeID: "allowed", OwnerID: 1, Name: "own", State: domain.SessionActive,
		CreatedAt: created, LiveSinceAt: created,
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := localMachine{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	actor := application.Principal{UserID: 1}
	if err := service.SelectNode(context.Background(), actor, "hidden"); err == nil {
		t.Fatal("hidden node selected")
	}
	if err := service.SelectSession(context.Background(), actor, session.Ref()); err != nil {
		t.Fatal(err)
	}
	got := machine.State().Navigation
	if got.ActiveNodeByUser[1] != "allowed" || got.ActiveSessionByUserNode[1]["allowed"] != "own" {
		t.Fatalf("navigation=%#v", got)
	}
}
