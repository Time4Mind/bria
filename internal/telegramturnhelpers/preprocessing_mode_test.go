package telegramturnhelpers

import (
	"context"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/settingsport"
)

type modePreferences struct {
	snapshot settingsport.Snapshot
}

func (p *modePreferences) Snapshot(context.Context) (settingsport.Snapshot, error) {
	return p.snapshot, nil
}

func (*modePreferences) ToggleContinueExisting(context.Context) error     { return nil }
func (*modePreferences) ToggleScreen(context.Context) error               { return nil }
func (*modePreferences) ToggleCardDetail(context.Context) error           { return nil }
func (*modePreferences) CycleCardPageLimit(context.Context) error         { return nil }
func (*modePreferences) ToggleTechnicalActions(context.Context) error     { return nil }
func (*modePreferences) ToggleBackgroundQuestions(context.Context) error  { return nil }
func (*modePreferences) ToggleBackgroundErrors(context.Context) error     { return nil }
func (*modePreferences) SetSessionLifetime(context.Context, string) error { return nil }

type capturingProcessor struct {
	request promptpreprocess.Request
	mode    promptpreprocess.Mode
	invalid bool
}

func (p *capturingProcessor) Process(_ context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	p.request = request
	return promptpreprocess.Result{Text: "clean text"}, nil
}

func (p *capturingProcessor) Invalidate(domain.ComputerID) { p.invalid = true }

func (p *capturingProcessor) SetMode(_ context.Context, mode promptpreprocess.Mode) error {
	p.mode = mode
	return nil
}

func TestReconfigureInvalidatesThenAppliesPersistedSatelliteMode(t *testing.T) {
	preferences := &modePreferences{snapshot: settingsport.Snapshot{
		SatellitePreprocessingMode: settingsport.SatellitePreprocessingPerSession,
	}}
	processor := &capturingProcessor{}
	preparation := Preparation{Settings: preferences, Processor: processor}
	if err := preparation.Reconfigure(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	if !processor.invalid || processor.mode != promptpreprocess.ModePerSession {
		t.Fatalf("reconfigure = invalidated %t, mode %q", processor.invalid, processor.mode)
	}
}

func TestPayloadSnapshotsSatelliteModeAtAdmission(t *testing.T) {
	preferences := &modePreferences{snapshot: settingsport.Snapshot{
		SatellitePreprocessingMode: settingsport.SatellitePreprocessingPerSession,
		PreprocessingEnabled:       true,
		PreprocessingInstruction:   "clean",
	}}
	processor := &capturingProcessor{}
	preparation := Preparation{Settings: preferences, Processor: processor, Timeout: time.Second}
	payload := preparation.Payload(context.Background(), "raw text")

	preferences.snapshot.SatellitePreprocessingMode = settingsport.SatellitePreprocessingShared
	session, err := domain.NewStartingSession("session", "intent", "local", domain.ProviderCodex, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	text, failed, prepared, _ := preparation.Process(context.Background(), session, "message", 1, payload)
	if failed || text != "clean text" || processor.request.Mode != promptpreprocess.ModePerSession {
		t.Fatalf("processed = (%q, %t), request mode = %q", text, failed, processor.request.Mode)
	}
	state, err := promptpreprocess.DecodeState(prepared)
	if err != nil || state.Mode != promptpreprocess.ModePerSession {
		t.Fatalf("prepared state = %#v, %v", state, err)
	}
}

func TestPayloadMapsLegacyBooleanAndExplicitDisabled(t *testing.T) {
	for _, test := range []struct {
		name     string
		snapshot settingsport.Snapshot
		want     promptpreprocess.Mode
	}{
		{name: "legacy enabled", snapshot: settingsport.Snapshot{PreprocessingEnabled: true}, want: promptpreprocess.ModeShared},
		{name: "legacy disabled", snapshot: settingsport.Snapshot{}, want: promptpreprocess.ModeDisabled},
		{name: "explicit disabled wins", snapshot: settingsport.Snapshot{PreprocessingEnabled: true, SatellitePreprocessingMode: settingsport.SatellitePreprocessingDisabled}, want: promptpreprocess.ModeDisabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			preparation := Preparation{Settings: &modePreferences{snapshot: test.snapshot}}
			payload := preparation.Payload(context.Background(), "raw text")
			state, err := promptpreprocess.DecodeState(payload)
			if err != nil || state.Mode != test.want {
				t.Fatalf("state = %#v, %v", state, err)
			}
		})
	}
}
