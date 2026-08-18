package application

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type healthLeadership struct {
	leader string
	stats  map[string]string
}

func (h healthLeadership) LeaderID() string         { return h.leader }
func (h healthLeadership) Stats() map[string]string { return h.stats }

func TestClusterHealthSurfacesOperationalEvidenceAndAgentTarget(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "alpha", Name: "Alpha", Status: domain.NodeOnline, Version: "v2", LastSeenAt: now,
			Backends: []domain.BackendDescriptor{{Name: "codex", Capabilities: []string{"session.create"}}}},
		{ID: "beta", Name: "Beta", Status: domain.NodeOffline, Version: "v1"},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	if err := state.AddSession(domain.Session{
		ID: "current", NodeID: "alpha", OwnerID: 7, Name: "Current", Backend: "codex",
		Workdir: "/work/pet_projects/bria", State: domain.SessionActive,
		CreatedAt: now.Add(-time.Minute), LiveSinceAt: now.Add(-time.Minute), LastEventAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	port := &capturePort{state: state}
	service, err := NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	service.SetLeadership(healthLeadership{
		leader: "alpha", stats: map[string]string{"commit_index": "100", "applied_index": "50"},
	})
	report, err := service.ClusterHealth(Principal{UserID: 7}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"node.offline": true, "node.version_drift": true, "raft.apply_lag": true}
	for _, finding := range report.Findings {
		delete(want, finding.Code)
	}
	if len(want) != 0 || report.LeaderID != "alpha" || report.Online != 1 || report.Enabled != 2 {
		t.Fatalf("report=%#v missing=%v", report, want)
	}
	if node, targetErr := service.ClusterAgentTarget(Principal{UserID: 7}); targetErr != nil || node.ID != "alpha" {
		t.Fatalf("agent target=%#v err=%v", node, targetErr)
	}
	if workdir, ok := service.ClusterAgentWorkdir(Principal{UserID: 7}, "alpha"); !ok || workdir != "/work/pet_projects/bria" {
		t.Fatalf("agent workdir=%q ok=%t", workdir, ok)
	}
	context := report.CompactContext()
	if context == "" || len(context) > 16<<10 {
		t.Fatalf("context length=%d", len(context))
	}
}
