package telegramcontroller_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
)

type creationEnvironmentStub struct {
	computers  []sessioncreation.Computer
	registered []sessioncreation.Computer
	roots      map[domain.ComputerID][]sessioncreation.Directory
	children   map[string][]sessioncreation.Directory
}

func localCreationEnvironment(t *testing.T, root string, capabilities ...sessioncreation.ProviderCapability) sessioncreation.Environment {
	t.Helper()
	environment, err := sessioncreation.NewLocalEnvironment("local", "Local", []string{root}, func(context.Context) ([]sessioncreation.ProviderCapability, error) {
		return append([]sessioncreation.ProviderCapability(nil), capabilities...), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return environment
}

func (environment *creationEnvironmentStub) AvailableComputers(context.Context) ([]sessioncreation.Computer, error) {
	return append([]sessioncreation.Computer(nil), environment.computers...), nil
}
func (environment *creationEnvironmentStub) RegisteredComputers(context.Context) ([]sessioncreation.Computer, error) {
	if environment.registered == nil {
		return append([]sessioncreation.Computer(nil), environment.computers...), nil
	}
	return append([]sessioncreation.Computer(nil), environment.registered...), nil
}
func (environment *creationEnvironmentStub) Roots(_ context.Context, computerID domain.ComputerID) ([]sessioncreation.Directory, error) {
	return append([]sessioncreation.Directory(nil), environment.roots[computerID]...), nil
}
func (environment *creationEnvironmentStub) Browse(_ context.Context, _ domain.ComputerID, path string) ([]sessioncreation.Directory, error) {
	children, ok := environment.children[path]
	if !ok {
		return nil, sessioncreation.ErrUnavailablePath
	}
	return append([]sessioncreation.Directory(nil), children...), nil
}
func (environment *creationEnvironmentStub) CreateChild(context.Context, domain.ComputerID, string, string) (string, error) {
	return "", errors.New("unexpected create child")
}
func (environment *creationEnvironmentStub) Parent(context.Context, domain.ComputerID, string) (string, bool) {
	return "", false
}
func (environment *creationEnvironmentStub) ToggleProvider(_ context.Context, computerID domain.ComputerID, provider domain.Provider) error {
	for computerIndex := range environment.computers {
		if environment.computers[computerIndex].ID != computerID {
			continue
		}
		for capabilityIndex := range environment.computers[computerIndex].Capabilities {
			capability := &environment.computers[computerIndex].Capabilities[capabilityIndex]
			if capability.Provider == provider && capability.Installed {
				capability.Enabled = !capability.Enabled
				return nil
			}
		}
	}
	return errors.New("provider is unavailable on node")
}

func TestSessionCreationV2UsesSelectedNodeWithoutComputerStep(t *testing.T) {
	environment := &creationEnvironmentStub{
		computers: []sessioncreation.Computer{
			{ID: "local", Name: "Coordinator", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}},
			{ID: "second", Name: "Second", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderClaude, Installed: true, Enabled: true}}},
		},
		roots: map[domain.ComputerID][]sessioncreation.Directory{
			"local":  {{Name: "/local", Path: "/local"}},
			"second": {{Name: "/work", Path: "/work"}},
		},
	}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{CreationEnvironment: environment})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	initial, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 1})
	if err != nil || initial.Surface == nil || initial.Surface.Text != "Новое\nВыберите корень." || initial.Surface.Rows[0][0].Label != "📁 /local" {
		t.Fatalf("coordinator creation = (%#v, %v)", initial, err)
	}
	nodes, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNodes, UpdateID: 2})
	if err != nil || nodes.Surface == nil || len(nodes.Surface.Rows) < 2 || !strings.Contains(nodes.Surface.Rows[0][0].Label, "Coordinator") {
		t.Fatalf("nodes = (%#v, %v)", nodes, err)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelectNode, Choice: 2, UpdateID: 3}); err != nil {
		t.Fatal(err)
	}
	next, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 4})
	if err != nil || next.Surface == nil || next.Surface.Text != "Новое\nВыберите корень." || next.Surface.Rows[0][0].Label != "📁 /work" {
		t.Fatalf("selected-node creation = (%#v, %v)", next, err)
	}
}

func TestSessionCreationV2CreatesWithSelectedRemoteNodeCapabilities(t *testing.T) {
	environment := &creationEnvironmentStub{
		computers: []sessioncreation.Computer{
			{ID: "local", Name: "Coordinator", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}},
			{ID: "worker", Name: "Worker", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderClaude, Installed: true, Enabled: true}}},
		},
		roots:    map[domain.ComputerID][]sessioncreation.Directory{"worker": {{Name: "/work", Path: "/work"}}},
		children: map[string][]sessioncreation.Directory{"/work": {}},
	}
	localProviders := &testProviderPreferences{values: map[domain.Provider]telegramcontroller.ProviderPreference{
		domain.ProviderCodex:  {Provider: domain.ProviderCodex, Configured: true, Enabled: true},
		domain.ProviderClaude: {Provider: domain.ProviderClaude, Configured: true, Enabled: false},
	}}
	var intent app.ConfirmedSessionIntent
	sessions := newLockedSessions()
	controller := newController(t, creatorFunc(func(_ context.Context, got app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		intent = got
		starting, createErr := domain.NewStartingSession("55555555-5555-4555-9555-555555555555", got.IntentID, got.ComputerID, got.Provider, got.Workdir)
		if createErr != nil {
			return app.CreateSessionResult{}, createErr
		}
		ready, readyErr := starting.Ready(domain.ProviderBinding{Provider: got.Provider, SessionID: "remote-provider", Generation: 1})
		if readyErr == nil {
			sessions.Set(ready)
		}
		return app.CreateSessionResult{Session: ready}, readyErr
	}), sessions, nil, nil, telegramcontroller.Options{CreationEnvironment: environment, Providers: localProviders})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelectNode, Choice: 2}); err != nil {
		t.Fatal(err)
	}
	root, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 1})
	if err != nil || root.Surface == nil || root.Surface.Rows[0][0].Label != "📁 /work" {
		t.Fatalf("remote root = (%#v, %v)", root, err)
	}
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateChoice, Choice: 1}); err != nil {
		t.Fatal(err)
	}
	created, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreatePick, UpdateID: 2})
	if err != nil || created.Card == nil {
		t.Fatalf("remote create = (%#v, %v)", created, err)
	}
	if intent.ComputerID != "worker" || intent.Provider != domain.ProviderClaude || intent.Workdir != "/work" {
		t.Fatalf("remote intent = %#v", intent)
	}
	if localProviders.values[domain.ProviderClaude].Enabled {
		t.Fatal("remote create mutated coordinator provider settings")
	}
}

func TestSessionCreationV2ActivatesInstalledBackendOnSelectedRemoteNode(t *testing.T) {
	environment := &creationEnvironmentStub{
		computers: []sessioncreation.Computer{
			{ID: "local", Name: "Coordinator", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}},
			{ID: "worker", Name: "Worker", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderClaude, Installed: true, Enabled: false}}},
		},
		roots: map[domain.ComputerID][]sessioncreation.Directory{"worker": {{Name: "/work", Path: "/work"}}},
	}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{CreationEnvironment: environment})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelectNode, Choice: 2}); err != nil {
		t.Fatal(err)
	}
	choice, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 1})
	if err != nil || choice.Surface == nil || !strings.Contains(choice.Surface.Rows[0][0].Label, "включить") {
		t.Fatalf("remote disabled backend = (%#v, %v)", choice, err)
	}
	next, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateSelectClaude, UpdateID: 2})
	if err != nil || next.Surface == nil || !strings.Contains(next.Surface.Text, "Выберите корень") || !environment.computers[1].Capabilities[0].Enabled {
		t.Fatalf("remote backend activation = (%#v, %v), environment=%#v", next, err, environment.computers)
	}
}

func TestSessionCreationV2BrowsesAndCreatesWithoutConfirmation(t *testing.T) {
	root := t.TempDir()
	environment, err := sessioncreation.NewLocalEnvironment("local", "Local", []string{root}, func(context.Context) ([]sessioncreation.ProviderCapability, error) {
		return []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var intent app.ConfirmedSessionIntent
	creates := 0
	sessions := newLockedSessions()
	controller := newController(t, creatorFunc(func(_ context.Context, got app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		creates++
		intent = got
		starting, createErr := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", got.IntentID, got.ComputerID, got.Provider, got.Workdir)
		if createErr != nil {
			return app.CreateSessionResult{}, createErr
		}
		ready, readyErr := starting.Ready(domain.ProviderBinding{Provider: got.Provider, SessionID: "provider", Generation: 1})
		if readyErr == nil {
			sessions.Set(ready)
		}
		return app.CreateSessionResult{Session: ready}, readyErr
	}), sessions, nil, nil, telegramcontroller.Options{CreationEnvironment: environment})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	initial, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 10})
	if err != nil || initial.Surface == nil || initial.Surface.Rows[0][0].Label != "📁 "+root {
		t.Fatalf("root choice = (%#v, %v)", initial, err)
	}
	opened, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateChoice, Choice: 1, UpdateID: 11})
	if err != nil || opened.Surface == nil || !hasSemanticAction(opened.Surface.Rows, telegramcontroller.SemanticCreatePick) {
		t.Fatalf("opened root = (%#v, %v)", opened, err)
	}
	created, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreatePick, UpdateID: 12})
	if err != nil || created.Card == nil {
		t.Fatalf("immediate create = (%#v, %v)", created, err)
	}
	if intent.IntentID != "telegram-update:12" || intent.ComputerID != "local" || intent.Provider != domain.ProviderCodex || intent.Workdir != root {
		t.Fatalf("created intent = %#v", intent)
	}
	repeated, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreatePick, UpdateID: 13})
	if err != nil || repeated.Card == nil || creates != 1 {
		t.Fatalf("repeated final callback = (%#v, %v), creates=%d", repeated, err, creates)
	}
}

func TestSessionCreationV2UsesValidPerComputerDefaults(t *testing.T) {
	root := t.TempDir()
	environment, err := sessioncreation.NewLocalEnvironment("local", "Local", []string{root}, func(context.Context) ([]sessioncreation.ProviderCapability, error) {
		return []sessioncreation.ProviderCapability{
			{Provider: domain.ProviderCodex, Installed: true, Enabled: true},
			{Provider: domain.ProviderClaude, Installed: true, Enabled: true},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	preferences := &testPreferences{settings: settingsport.Snapshot{
		ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64, QueueLimit: 32,
		DefaultProviders: map[domain.ComputerID]domain.Provider{"local": domain.ProviderClaude},
		DefaultWorkdirs:  map[domain.ComputerID]string{"local": root},
	}}
	var intent app.ConfirmedSessionIntent
	sessions := newLockedSessions()
	controller := newController(t, creatorFunc(func(_ context.Context, got app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
		intent = got
		starting, createErr := domain.NewStartingSession("22222222-2222-4222-9222-222222222222", got.IntentID, got.ComputerID, got.Provider, got.Workdir)
		if createErr != nil {
			return app.CreateSessionResult{}, createErr
		}
		ready, readyErr := starting.Ready(domain.ProviderBinding{Provider: got.Provider, SessionID: "provider", Generation: 1})
		if readyErr == nil {
			sessions.Set(ready)
		}
		return app.CreateSessionResult{Session: ready}, readyErr
	}), sessions, nil, nil, telegramcontroller.Options{Settings: preferences, CreationEnvironment: environment})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	created, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 21})
	if err != nil || created.Card == nil {
		t.Fatalf("defaulted create = (%#v, %v)", created, err)
	}
	if intent.Provider != domain.ProviderClaude || intent.Workdir != root {
		t.Fatalf("defaulted intent = %#v", intent)
	}
}

func TestSessionCreationV2EnablesInstalledBackendInline(t *testing.T) {
	providers := &testProviderPreferences{values: map[domain.Provider]telegramcontroller.ProviderPreference{
		domain.ProviderCodex:  {Provider: domain.ProviderCodex, Configured: true, Enabled: false},
		domain.ProviderClaude: {Provider: domain.ProviderClaude, Configured: false, Enabled: false},
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{Providers: providers})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	initial, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 30})
	if err != nil || initial.Surface == nil || !strings.Contains(initial.Surface.Text, "Выберите бэкенд") || !strings.Contains(initial.Surface.Rows[0][0].Label, "включить") {
		t.Fatalf("disabled provider choice = (%#v, %v)", initial, err)
	}
	next, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateSelectCodex, UpdateID: 31})
	if err != nil || next.Surface == nil || !strings.Contains(next.Surface.Text, "Выберите корень") || !providers.values[domain.ProviderCodex].Enabled {
		t.Fatalf("inline provider enable = (%#v, %v), preferences=%#v", next, err, providers.values)
	}
}

func TestSessionCreationV2RoutesMissingBackendsToSettings(t *testing.T) {
	providers := &testProviderPreferences{values: map[domain.Provider]telegramcontroller.ProviderPreference{
		domain.ProviderCodex:  {Provider: domain.ProviderCodex},
		domain.ProviderClaude: {Provider: domain.ProviderClaude},
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{Providers: providers})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 40})
	if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, "не установлен") || !hasSemanticAction(result.Surface.Rows, telegramcontroller.SemanticMenuSettings) {
		t.Fatalf("missing backend surface = (%#v, %v)", result, err)
	}
}

func TestSessionCreationV2CreatesChildAndKeepsBrowserOpen(t *testing.T) {
	root := t.TempDir()
	environment := localCreationEnvironment(t, root, sessioncreation.ProviderCapability{Provider: domain.ProviderCodex, Installed: true, Enabled: true})
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{CreationEnvironment: environment})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	_, _ = controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 50})
	_, _ = controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateChoice, Choice: 1, UpdateID: 51})
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateDirectoryNew}); err != nil {
		t.Fatal(err)
	}
	result, err := controller.HandleSemanticMessage(context.Background(), message(52, "child"))
	if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, root+"/child") || !hasSemanticAction(result.Surface.Rows, telegramcontroller.SemanticCreatePick) {
		t.Fatalf("created child browser = (%#v, %v)", result, err)
	}
}

func TestSessionCreationV2OffersExactArchiveContinuation(t *testing.T) {
	root := t.TempDir()
	archived := archivedSession(t, "33333333-3333-4333-9333-333333333333", domain.ProviderCodex, root, "provider-archive", 1)
	sessions := newLockedSessions(archived)
	preferences := &testPreferences{settings: settingsport.Snapshot{
		ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64, QueueLimit: 32,
		ArchiveRecommendations: true,
	}}
	resumer := archivedResumerFunc(func(_ context.Context, id domain.SessionID) (domain.Session, error) {
		if id != archived.ID() {
			return domain.Session{}, errors.New("wrong archive")
		}
		resuming, beginErr := archived.BeginResume(archived.StateChangedAt().Add(time.Minute))
		if beginErr != nil {
			return domain.Session{}, beginErr
		}
		resumed, readyErr := resuming.ResumeReady(
			domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-archive", Generation: 2},
			archived.StateChangedAt().Add(2*time.Minute), domain.SessionLifetimeNever,
		)
		if readyErr == nil {
			sessions.Set(resumed)
		}
		return resumed, readyErr
	})
	controller := newController(t, nil, sessions, nil, nil, telegramcontroller.Options{
		Settings: preferences, ArchivedResumer: resumer,
		CreationEnvironment: localCreationEnvironment(t, root, sessioncreation.ProviderCapability{Provider: domain.ProviderCodex, Installed: true, Enabled: true}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	_, _ = controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 60})
	_, _ = controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateChoice, Choice: 1, UpdateID: 61})
	recommendations, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreatePick, UpdateID: 62})
	if err != nil || recommendations.Surface == nil || !strings.Contains(recommendations.Surface.Text, "архивную") || !hasSemanticAction(recommendations.Surface.Rows, telegramcontroller.SemanticCreateFresh) {
		t.Fatalf("archive recommendations = (%#v, %v)", recommendations, err)
	}
	resumed, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateChoice, Choice: 1, UpdateID: 63})
	if err != nil || resumed.Card == nil || resumed.Card.SessionID != archived.ID() {
		t.Fatalf("exact archive continuation = (%#v, %v)", resumed, err)
	}
}

func TestSessionCreationDefaultsCanBeChosenAndClearedThroughSettings(t *testing.T) {
	root := t.TempDir()
	preferences := &testPreferences{}
	environment := localCreationEnvironment(t, root,
		sessioncreation.ProviderCapability{Provider: domain.ProviderCodex, Installed: true, Enabled: true},
		sessioncreation.ProviderCapability{Provider: domain.ProviderClaude, Installed: true, Enabled: true},
	)
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{Settings: preferences, CreationEnvironment: environment})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	providerChoice, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsDefaultProvider, UpdateID: 70})
	if err != nil || providerChoice.Surface == nil || !strings.Contains(providerChoice.Surface.Text, "Выберите бэкенд") {
		t.Fatalf("default provider choice = (%#v, %v)", providerChoice, err)
	}
	settingsSurface, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateSelectClaude, UpdateID: 71})
	if err != nil || settingsSurface.Surface == nil || !strings.Contains(settingsSurface.Surface.Text, "Backend по умолчанию (local): Claude") {
		t.Fatalf("saved default provider = (%#v, %v)", settingsSurface, err)
	}

	directoryChoice, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsDefaultWorkdir, UpdateID: 72})
	if err != nil || directoryChoice.Surface == nil || directoryChoice.Surface.Rows[0][0].Label != "📁 "+root {
		t.Fatalf("default directory roots = (%#v, %v)", directoryChoice, err)
	}
	_, _ = controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreateChoice, Choice: 1, UpdateID: 73})
	settingsSurface, err = controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticCreatePick, UpdateID: 74})
	if err != nil || settingsSurface.Surface == nil || !strings.Contains(settingsSurface.Surface.Text, "Папка по умолчанию: "+root) {
		t.Fatalf("saved default workdir = (%#v, %v)", settingsSurface, err)
	}

	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsClearCreationDefaults, UpdateID: 75}); err != nil {
		t.Fatal(err)
	}
	persisted, err := preferences.Snapshot(context.Background())
	if err != nil || len(persisted.DefaultProviders) != 0 || len(persisted.DefaultWorkdirs) != 0 {
		t.Fatalf("cleared creation defaults = (%#v, %v)", persisted, err)
	}
}
