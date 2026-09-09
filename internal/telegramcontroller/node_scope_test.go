package telegramcontroller_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramsettingsview"
	"bria/internal/telegramstatus"
)

type quotaReaderStub struct{ snapshots []telegramstatus.Snapshot }

func (stub quotaReaderStub) Snapshots(context.Context) ([]telegramstatus.Snapshot, error) {
	return append([]telegramstatus.Snapshot(nil), stub.snapshots...), nil
}

type blockingQuotaReader struct {
	started chan struct{}
}

func (reader *blockingQuotaReader) Snapshots(context.Context) ([]telegramstatus.Snapshot, error) {
	return nil, nil
}

func (reader *blockingQuotaReader) Refresh(ctx context.Context) error {
	close(reader.started)
	<-ctx.Done()
	return ctx.Err()
}

type nodeUIStateStub struct {
	selected domain.ComputerID
	active   map[domain.ComputerID]domain.SessionID
}

func (state *nodeUIStateStub) SetActiveSession(_ context.Context, sessionID domain.SessionID) error {
	if state.active == nil {
		state.active = make(map[domain.ComputerID]domain.SessionID)
	}
	state.active[state.selected] = sessionID
	return nil
}

func (state *nodeUIStateStub) LoadActiveSession(context.Context) (domain.SessionID, error) {
	return state.active[state.selected], nil
}

func (state *nodeUIStateStub) LoadNodeSelection(context.Context) (domain.ComputerID, map[domain.ComputerID]domain.SessionID, error) {
	result := make(map[domain.ComputerID]domain.SessionID, len(state.active))
	for nodeID, sessionID := range state.active {
		result[nodeID] = sessionID
	}
	return state.selected, result, nil
}

func (state *nodeUIStateStub) SetSelectedNode(_ context.Context, nodeID domain.ComputerID) error {
	state.selected = nodeID
	return nil
}

func (state *nodeUIStateStub) SetNodeActiveSession(_ context.Context, nodeID domain.ComputerID, sessionID domain.SessionID) error {
	if state.active == nil {
		state.active = make(map[domain.ComputerID]domain.SessionID)
	}
	state.selected = nodeID
	state.active[nodeID] = sessionID
	return nil
}

func (state *nodeUIStateStub) ClearNodeActiveSession(_ context.Context, nodeID domain.ComputerID) error {
	delete(state.active, nodeID)
	return nil
}

func TestNodeMenuKeepsCoordinatorFirstAndMarksUnavailableNode(t *testing.T) {
	environment := &creationEnvironmentStub{
		computers: []sessioncreation.Computer{{
			ID: "local", Name: "Zeta",
		}},
		registered: []sessioncreation.Computer{
			{ID: "worker", Name: "Alpha"},
			{ID: "local", Name: "Zeta"},
		},
	}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{CreationEnvironment: environment})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticMenuNodes, UpdateID: 1,
	})
	if err != nil || result.Surface == nil || len(result.Surface.Rows) != 3 {
		t.Fatalf("node menu = %#v, err=%v", result, err)
	}
	if got := result.Surface.Rows[0][0].Label; !strings.Contains(got, "Zeta") || !strings.Contains(got, "координатор") || !strings.HasPrefix(got, "✓ ") {
		t.Fatalf("first node label = %q", got)
	}
	if got := result.Surface.Rows[1][0].Label; !strings.Contains(got, "Alpha") || !strings.Contains(got, "недоступна") {
		t.Fatalf("unavailable node label = %q", got)
	}
}

func TestStatusKeepsAgreedNodeButtonsAndAddsLegacyQuotaTable(t *testing.T) {
	now := time.Now().UTC()
	environment := &creationEnvironmentStub{computers: []sessioncreation.Computer{
		{ID: "local", Name: "Coordinator"}, {ID: "worker", Name: "Worker"},
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{
		CreationEnvironment: environment,
		Quotas: quotaReaderStub{snapshots: []telegramstatus.Snapshot{{
			ComputerID: "local", Provider: domain.ProviderCodex,
			Weekly: &telegramstatus.Window{UsedPercent: 50, ResetsAt: now.Add(24 * time.Hour)}, CollectedAt: now,
		}}},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	nodes, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNodes})
	if err != nil {
		t.Fatal(err)
	}
	status, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuStatus})
	if nodes.Surface == nil || status.Surface == nil || nodes.Surface.Text != status.Surface.Text || nodes.Surface.RichMarkdown != status.Surface.RichMarkdown {
		t.Fatalf("nodes and status must show identical content: nodes=%#v status=%#v", nodes.Surface, status.Surface)
	}
	if err != nil || status.Surface == nil || !strings.Contains(status.Surface.Text, "| Сервер | Бэк | Израсх. | Остаток | Обновлено | Сброс |") ||
		!strings.Contains(status.Surface.Text, "| 👑 Coordinator | codex | w 50%") {
		t.Fatalf("status = %#v, err=%v", status, err)
	}
	if len(nodes.Surface.Rows) != len(status.Surface.Rows) {
		t.Fatalf("status buttons changed: nodes=%#v status=%#v", nodes.Surface.Rows, status.Surface.Rows)
	}
	for index := range nodes.Surface.Rows {
		if nodes.Surface.Rows[index][0].Action != status.Surface.Rows[index][0].Action || nodes.Surface.Rows[index][0].Label != status.Surface.Rows[index][0].Label {
			t.Fatalf("status button %d differs: nodes=%#v status=%#v", index, nodes.Surface.Rows[index], status.Surface.Rows[index])
		}
	}
}

func TestStatusRefreshCallbackDefersSlowProviderUntilPostCommitRunner(t *testing.T) {
	reader := &blockingQuotaReader{started: make(chan struct{})}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{
		CreationEnvironment: &creationEnvironmentStub{computers: []sessioncreation.Computer{{ID: "local", Name: "Coordinator"}}},
		Quotas:              reader,
	})
	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticRefreshStatus})
	if err != nil || result.Surface == nil {
		t.Fatalf("refresh result = %#v, err=%v", result, err)
	}
	select {
	case <-reader.started:
		t.Fatal("quota refresh started before cached callback projection was committed")
	default:
	}
	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := controller.RefreshStatus(context.Background())
		refreshDone <- refreshErr
	}()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("quota refresh was not started")
	}
	started := time.Now()
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuBack}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("navigation blocked behind quota refresh for %s", elapsed)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-refreshDone:
		if err == nil {
			t.Fatal("cancelled quota refresh returned no error")
		}
	case <-time.After(time.Second):
		t.Fatal("quota refresh did not stop with controller")
	}
}

func TestNodeSelectionRestoresPerNodeActiveAndScopesCardSessions(t *testing.T) {
	localFirst := readySessionOnNode(t, "11111111-1111-4111-9111-111111111111", "local")
	localSecond := readySessionOnNode(t, "22222222-2222-4222-9222-222222222222", "local")
	remote := readySessionOnNode(t, "33333333-3333-4333-9333-333333333333", "worker")
	sessions := newLockedSessions(localFirst, localSecond, remote)
	ui := &nodeUIStateStub{
		selected: "local",
		active: map[domain.ComputerID]domain.SessionID{
			"local": localFirst.ID(), "worker": remote.ID(),
		},
	}
	environment := &creationEnvironmentStub{
		computers: []sessioncreation.Computer{
			{ID: "local", Name: "Coordinator"},
			{ID: "worker", Name: "Worker"},
		},
	}
	controller := newController(t, nil, sessions, nil, nil, telegramcontroller.Options{
		UIState: ui, CreationEnvironment: environment,
		Recovered: []domain.Session{localFirst, localSecond, remote},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	current, err := controller.ProjectCurrent(context.Background(), "")
	if err != nil || current.Card == nil || current.Card.SessionID != localFirst.ID() ||
		len(current.Card.SelectableSessionIDs) != 2 {
		t.Fatalf("local current = %#v, err=%v", current, err)
	}
	switched, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSelectNode, Choice: 2, UpdateID: 2,
	})
	if err != nil || switched.Card == nil || switched.Card.SessionID != remote.ID() {
		t.Fatalf("worker switch = %#v, err=%v", switched, err)
	}
	if ui.selected != "worker" || len(switched.Card.SelectableSessionIDs) != 1 ||
		switched.Card.SelectableSessionIDs[0] != remote.ID() {
		t.Fatalf("worker state = selected %q, sessions %#v", ui.selected, switched.Card.SelectableSessionIDs)
	}
}

func TestUnavailableRestoredNodeFallsBackToCoordinator(t *testing.T) {
	local := readySessionOnNode(t, "11111111-1111-4111-9111-111111111111", "local")
	remote := readySessionOnNode(t, "22222222-2222-4222-9222-222222222222", "worker")
	ui := &nodeUIStateStub{
		selected: "worker",
		active: map[domain.ComputerID]domain.SessionID{
			"local": local.ID(), "worker": remote.ID(),
		},
	}
	environment := &creationEnvironmentStub{
		computers: []sessioncreation.Computer{{ID: "local", Name: "Coordinator"}},
		registered: []sessioncreation.Computer{
			{ID: "local", Name: "Coordinator"},
			{ID: "worker", Name: "Worker"},
		},
	}
	controller := newController(t, nil, newLockedSessions(local, remote), nil, nil, telegramcontroller.Options{
		UIState: ui, CreationEnvironment: environment, Recovered: []domain.Session{local, remote},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	current, err := controller.ProjectCurrent(context.Background(), "")
	if err != nil || current.Card == nil || current.Card.SessionID != local.ID() {
		t.Fatalf("fallback current = %#v, err=%v", current, err)
	}
	if ui.selected != "local" {
		t.Fatalf("persisted fallback node = %q, want local", ui.selected)
	}
}

func TestSessionAndArchiveSurfacesAreScopedToSelectedNode(t *testing.T) {
	localOpen := readySessionOnNode(t, "11111111-1111-4111-9111-111111111111", "local")
	remoteOpen := readySessionOnNode(t, "22222222-2222-4222-9222-222222222222", "worker")
	localArchived := archiveNodeSession(t, readySessionOnNode(t, "33333333-3333-4333-9333-333333333333", "local"))
	remoteArchived := archiveNodeSession(t, readySessionOnNode(t, "44444444-4444-4444-9444-444444444444", "worker"))
	environment := &creationEnvironmentStub{computers: []sessioncreation.Computer{
		{ID: "local", Name: "Coordinator"},
		{ID: "worker", Name: "Worker"},
	}}
	controller := newController(t, nil, newLockedSessions(localOpen, remoteOpen, localArchived, remoteArchived), nil, nil, telegramcontroller.Options{
		CreationEnvironment: environment, Recovered: []domain.Session{localOpen, remoteOpen},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	open, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticMenuSessions, UpdateID: 1,
	})
	if err != nil || open.Card == nil || open.Card.SessionID != localOpen.ID() || len(open.Card.SelectableSessionIDs) != 1 ||
		open.Card.SelectableSessionIDs[0] != localOpen.ID() || open.Surface != nil {
		t.Fatalf("local sessions = %#v, err=%v", open, err)
	}
	archive, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticMenuArchive, UpdateID: 2,
	})
	if err != nil || archive.Surface == nil || len(archive.Surface.Rows) != 3 ||
		archive.Surface.Rows[0][0].SessionID != localArchived.ID() {
		t.Fatalf("local archive = %#v, err=%v", archive, err)
	}
}

func TestSessionButtonsUseUniqueTenCharacterDirectoryNamesAndMarkActive(t *testing.T) {
	makeSession := func(id, workdir string) domain.Session {
		starting, err := domain.NewStartingSession(domain.SessionID(id), domain.IntentID("intent-"+id), "local", domain.ProviderCodex, workdir)
		if err != nil {
			t.Fatal(err)
		}
		ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + id, Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		return ready
	}
	first := makeSession("11111111-1111-4111-9111-111111111111", "/a/longdirectory")
	second := makeSession("22222222-2222-4222-9222-222222222222", "/b/longdirectory")
	controller := newController(t, nil, newLockedSessions(first, second), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{first, second}})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	result, err := controller.ProjectCurrent(context.Background(), first.ID())
	if err != nil || result.Card == nil {
		t.Fatalf("card = %#v, err=%v", result, err)
	}
	want := []string{"✓ longdirect", "longdirec2"}
	if len(result.Card.SelectableSessionLabels) != len(want) {
		t.Fatalf("labels = %#v", result.Card.SelectableSessionLabels)
	}
	for index := range want {
		if result.Card.SelectableSessionLabels[index] != want[index] {
			t.Fatalf("label %d = %q, want %q", index, result.Card.SelectableSessionLabels[index], want[index])
		}
	}
}

func TestStaleSessionAndArchiveActionsCannotCrossSelectedNode(t *testing.T) {
	localOpen := readySessionOnNode(t, "11111111-1111-4111-9111-111111111111", "local")
	remoteOpen := readySessionOnNode(t, "22222222-2222-4222-9222-222222222222", "worker")
	remoteArchived := archiveNodeSession(t, readySessionOnNode(t, "33333333-3333-4333-9333-333333333333", "worker"))
	environment := &creationEnvironmentStub{computers: []sessioncreation.Computer{
		{ID: "local", Name: "Coordinator"},
		{ID: "worker", Name: "Worker"},
	}}
	controller := newController(t, nil, newLockedSessions(localOpen, remoteOpen, remoteArchived), nil, nil, telegramcontroller.Options{
		CreationEnvironment: environment, Recovered: []domain.Session{localOpen, remoteOpen},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSelect, SessionID: remoteOpen.ID(), UpdateID: 1,
	}); err == nil || !strings.Contains(err.Error(), "selected node") {
		t.Fatalf("cross-node select error = %v", err)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticResume, SessionID: remoteArchived.ID(), UpdateID: 2,
	}); err == nil || !strings.Contains(err.Error(), "selected node") {
		t.Fatalf("cross-node resume error = %v", err)
	}
}

func TestNewOnUnavailableSelectedNodeOpensNodeMenuWithoutDraft(t *testing.T) {
	remote := readySessionOnNode(t, "22222222-2222-4222-9222-222222222222", "worker")
	ui := &nodeUIStateStub{
		selected: "worker",
		active: map[domain.ComputerID]domain.SessionID{
			"worker": remote.ID(),
		},
	}
	environment := &creationEnvironmentStub{
		computers: []sessioncreation.Computer{
			{ID: "local", Name: "Coordinator"},
			{ID: "worker", Name: "Worker"},
		},
		registered: []sessioncreation.Computer{{ID: "local", Name: "Coordinator"}, {ID: "worker", Name: "Worker"}},
	}
	controller := newController(t, nil, newLockedSessions(remote), nil, nil, telegramcontroller.Options{
		UIState: ui, CreationEnvironment: environment, Recovered: []domain.Session{remote},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	environment.computers = []sessioncreation.Computer{{ID: "local", Name: "Coordinator"}}
	nodes, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticMenuNew, UpdateID: 1,
	})
	if err != nil || nodes.Surface == nil || !strings.HasPrefix(nodes.Surface.Text, "Ноды\n") || !nodes.Surface.RichMarkdown || ui.selected != "worker" {
		t.Fatalf("unavailable selection = %#v, selected=%q, err=%v", nodes, ui.selected, err)
	}
}

func TestCurrentCardOnUnavailableSelectedNodeOpensNodeMenu(t *testing.T) {
	remote := readySessionOnNode(t, "22222222-2222-4222-9222-222222222222", "worker")
	ui := &nodeUIStateStub{
		selected: "worker",
		active: map[domain.ComputerID]domain.SessionID{
			"worker": remote.ID(),
		},
	}
	environment := &creationEnvironmentStub{
		computers: []sessioncreation.Computer{
			{ID: "local", Name: "Coordinator"},
			{ID: "worker", Name: "Worker"},
		},
		registered: []sessioncreation.Computer{{ID: "local", Name: "Coordinator"}, {ID: "worker", Name: "Worker"}},
	}
	controller := newController(t, nil, newLockedSessions(remote), nil, nil, telegramcontroller.Options{
		UIState: ui, CreationEnvironment: environment, Recovered: []domain.Session{remote},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	environment.computers = []sessioncreation.Computer{{ID: "local", Name: "Coordinator"}}
	result, err := controller.ProjectCurrent(context.Background(), "")
	if err != nil || result.Surface == nil || !strings.HasPrefix(result.Surface.Text, "Ноды\n") || !result.Surface.RichMarkdown || result.Card != nil {
		t.Fatalf("unavailable current projection = %#v, err=%v", result, err)
	}
	if ui.selected != "worker" {
		t.Fatalf("selected node = %q, want worker until explicit switch", ui.selected)
	}
}

func TestClearingCreationDefaultsAffectsOnlySelectedNode(t *testing.T) {
	preferences := &testPreferences{}
	if _, err := preferences.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	preferences.settings.DefaultProviders = map[domain.ComputerID]domain.Provider{
		"local": domain.ProviderCodex, "worker": domain.ProviderClaude,
	}
	preferences.settings.DefaultWorkdirs = map[domain.ComputerID]string{
		"local": "/local", "worker": "/worker",
	}
	environment := &creationEnvironmentStub{computers: []sessioncreation.Computer{
		{ID: "local", Name: "Coordinator"},
		{ID: "worker", Name: "Worker"},
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{
		Settings: preferences, CreationEnvironment: environment,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSelectNode, Choice: 2, UpdateID: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSettingsClearCreationDefaults, UpdateID: 2,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := preferences.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultProviders["local"] != domain.ProviderCodex || got.DefaultWorkdirs["local"] != "/local" {
		t.Fatalf("coordinator defaults changed: %#v %#v", got.DefaultProviders, got.DefaultWorkdirs)
	}
	if got.DefaultProviders["worker"] != "" || got.DefaultWorkdirs["worker"] != "" {
		t.Fatalf("worker defaults not cleared: %#v %#v", got.DefaultProviders, got.DefaultWorkdirs)
	}
}

func TestProviderSettingsReadAndToggleOnlySelectedNode(t *testing.T) {
	localProviders := &testProviderPreferences{values: map[domain.Provider]telegramcontroller.ProviderPreference{
		domain.ProviderCodex:  {Provider: domain.ProviderCodex, Configured: true, Enabled: true},
		domain.ProviderClaude: {Provider: domain.ProviderClaude, Configured: false, Enabled: false},
	}}
	environment := &creationEnvironmentStub{computers: []sessioncreation.Computer{
		{ID: "local", Name: "Coordinator", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}},
		{ID: "worker", Name: "Worker", Capabilities: []sessioncreation.ProviderCapability{
			{Provider: domain.ProviderCodex, Installed: false, Enabled: false},
			{Provider: domain.ProviderClaude, Installed: true, Enabled: false},
		}},
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{
		Providers: localProviders, CreationEnvironment: environment,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSelectNode, Choice: 2,
	}); err != nil {
		t.Fatal(err)
	}
	settings, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSettingsCategory, Choice: int(telegramsettingsview.CategoryProviders),
	})
	if err != nil || settings.Surface == nil || !strings.Contains(settings.Surface.Text, "| claude | выключен, настроен |") ||
		!strings.Contains(settings.Surface.Text, "| codex | выключен, не настроен |") {
		t.Fatalf("worker provider settings = %#v, err=%v", settings, err)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSettingsProviderClaude,
	}); err != nil {
		t.Fatal(err)
	}
	if !environment.computers[1].Capabilities[1].Enabled {
		t.Fatal("worker Claude was not enabled")
	}
	if localProviders.values[domain.ProviderClaude].Enabled {
		t.Fatal("worker settings changed coordinator Claude")
	}
}

func readySessionOnNode(t *testing.T, id string, nodeID domain.ComputerID) domain.Session {
	t.Helper()
	starting, err := domain.NewStartingSession(domain.SessionID(id), domain.IntentID("intent-"+id), nodeID, domain.ProviderCodex, "/work")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + id, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func archiveNodeSession(t *testing.T, session domain.Session) domain.Session {
	t.Helper()
	closing, err := session.BeginClose(session.StateChangedAt().Add(1))
	if err != nil {
		t.Fatal(err)
	}
	archived, err := closing.Archive(closing.StateChangedAt().Add(1))
	if err != nil {
		t.Fatal(err)
	}
	return archived
}
