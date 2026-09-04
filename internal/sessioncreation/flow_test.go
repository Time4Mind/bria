package sessioncreation_test

import (
	"testing"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
)

func TestFlowAlwaysShowsMultipleComputersAndUsesValidPerComputerDefaults(t *testing.T) {
	computers := []sessioncreation.Computer{
		{ID: "first", Name: "First", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}},
		{ID: "second", Name: "Second", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}, {Provider: domain.ProviderClaude, Installed: true, Enabled: true}}},
	}
	defaults := sessioncreation.Defaults{
		Providers: map[domain.ComputerID]domain.Provider{"second": domain.ProviderClaude},
		Workdirs:  map[domain.ComputerID]string{"second": "/work"},
	}
	flow := sessioncreation.New()
	if initial := flow.BeginV2(computers, defaults); initial.Step != sessioncreation.StepComputer {
		t.Fatalf("initial step = %q", initial.Step)
	}
	if err := flow.SelectComputer(2, defaults); err != nil {
		t.Fatal(err)
	}
	current, ok := flow.CurrentV2(computers, defaults)
	if !ok || current.Step != sessioncreation.StepDirectory || current.Draft.Provider != domain.ProviderClaude || current.Draft.Workdir != "/work" {
		t.Fatalf("selected snapshot = %#v, open=%t", current, ok)
	}
}

func TestFlowSeparatesInstalledAndEnabledProviders(t *testing.T) {
	defaults := sessioncreation.Defaults{}
	flow := sessioncreation.New()
	computer := sessioncreation.Computer{ID: "local", Name: "Local", Capabilities: []sessioncreation.ProviderCapability{
		{Provider: domain.ProviderCodex, Installed: true, Enabled: false},
		{Provider: domain.ProviderClaude, Installed: false, Enabled: false},
	}}
	if initial := flow.BeginV2([]sessioncreation.Computer{computer}, defaults); initial.Step != sessioncreation.StepProvider {
		t.Fatalf("installed disabled provider step = %q", initial.Step)
	}
	if err := flow.SelectProviderV2(domain.ProviderCodex, defaults); err == nil {
		t.Fatal("disabled provider was selectable")
	}
	computer.Capabilities[0].Enabled = true
	current, _ := flow.CurrentV2([]sessioncreation.Computer{computer}, defaults)
	if current.Step != sessioncreation.StepProvider {
		t.Fatalf("current step = %q, want explicit provider step retained", current.Step)
	}
	if err := flow.SelectProviderV2(domain.ProviderCodex, defaults); err != nil {
		t.Fatal(err)
	}
}

func TestFlowPaginatesDirectoriesAndRecommendationsAndBackTracksShownSteps(t *testing.T) {
	flow := sessioncreation.New()
	defaults := sessioncreation.Defaults{ArchiveRecommendations: true}
	computer := sessioncreation.Computer{ID: "local", Name: "Local", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}}
	flow.BeginV2([]sessioncreation.Computer{computer}, defaults)
	directories := make([]sessioncreation.Directory, 10)
	for index := range directories {
		directories[index] = sessioncreation.Directory{Name: string(rune('a' + index)), Path: "/root/" + string(rune('a'+index))}
	}
	if err := flow.SetDirectoryListing("/root", directories); err != nil {
		t.Fatal(err)
	}
	second := flow.Page(1, false)
	if second.Page != 2 || second.Pages != 2 || len(second.Directories) != 2 {
		t.Fatalf("second directory page = %#v", second)
	}
	if err := flow.SelectWorkdir("/root/a"); err != nil {
		t.Fatal(err)
	}
	recommendations := make([]sessioncreation.Recommendation, 10)
	for index := range recommendations {
		recommendations[index] = sessioncreation.Recommendation{SessionID: domain.SessionID("session"), Label: "item"}
	}
	if err := flow.SetRecommendations(recommendations); err != nil {
		t.Fatal(err)
	}
	if got := flow.Page(-1, false); got.Page != 2 || len(got.Recommendations) != 2 {
		t.Fatalf("wrapped recommendation page = %#v", got)
	}
	back, ok := flow.Back()
	if !ok || back.Step != sessioncreation.StepDirectory || back.Draft.Workdir != "" {
		t.Fatalf("Back() = %#v, %t", back, ok)
	}
}

func TestFlowConsumesOnlyValidStandaloneDirectoryName(t *testing.T) {
	flow := sessioncreation.New()
	computer := sessioncreation.Computer{ID: "local", Name: "Local", Capabilities: []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}}
	flow.BeginV2([]sessioncreation.Computer{computer}, sessioncreation.Defaults{})
	if err := flow.SetDirectoryListing("/root", nil); err != nil {
		t.Fatal(err)
	}
	if err := flow.BeginDirectoryName(); err != nil {
		t.Fatal(err)
	}
	before := flow.Revision()
	if name, consumed := flow.ConsumeDirectoryName("bad/name", false); !consumed || name != "" || flow.Revision() != before+1 {
		t.Fatalf("invalid name result = %q, %t", name, consumed)
	}
	if name, consumed := flow.ConsumeDirectoryName("project", false); !consumed || name != "project" {
		t.Fatalf("valid name result = %q, %t", name, consumed)
	}
}
