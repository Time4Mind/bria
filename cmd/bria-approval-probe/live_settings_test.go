package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/nativeapproval"
	"bria/internal/nativeapprovalflow"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
)

// This opt-in acceptance test consumes exactly one independently inspected,
// read-only pending dialog. Ordinary test runs never connect to live tmux.
func TestLivePendingIncompleteApprovalHonorsDurableOffThenOn(t *testing.T) {
	pane := os.Getenv("BRIA_LIVE_APPROVAL_PANE")
	if pane == "" {
		t.Skip("explicit live read-only approval probe only")
	}
	identity, fingerprint := os.Getenv("BRIA_LIVE_APPROVAL_IDENTITY"), os.Getenv("BRIA_LIVE_APPROVAL_FINGERPRINT")
	if identity == "" || fingerprint == "" {
		t.Fatal("exact inspected identity and fingerprint required")
	}
	ctx := context.Background()
	term := &terminal{target: pane, identity: identity}
	screen, err := term.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request, ok := nativeapproval.ParseCodexCommandApproval(screen)
	if !ok || !request.Incomplete || request.Fingerprint != fingerprint {
		t.Fatal("expected pending incomplete approval not present")
	}
	store, err := settings.OpenFileStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	prefs := settingscomposition.Preferences{Store: store}
	enabled := func(ctx context.Context) (bool, error) { s, e := prefs.Snapshot(ctx); return s.AutoApproveCommands, e }
	flow := &nativeapprovalflow.Flow{}
	probe := &liveApprovalDriver{term: term, snapshot: sessionruntime.NativeSnapshot{FullText: screen, Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(screen))), ProviderSessionID: identity, Generation: 1, Interactive: true}, fingerprint: fingerprint}
	if err := flow.Toggle(ctx, prefs.ToggleAutoApproveCommands); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := flow.Observe(ctx, "live-probe", probe, probe, enabled); err != nil {
			t.Fatal(err)
		}
	}
	still, err := term.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := nativeapproval.ParseCodexCommandApproval(still)
	if !ok || got.Fingerprint != fingerprint || probe.calls != 0 {
		t.Fatal("OFF did not retain pending request without input")
	}
	t.Log("OFF: three observations, zero native controls, exact incomplete request retained")
	if err := flow.Toggle(ctx, prefs.ToggleAutoApproveCommands); err != nil {
		t.Fatal(err)
	}
	if err := flow.Observe(ctx, "live-probe", probe, probe, enabled); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 {
		t.Fatal("expected exactly one native control")
	}
	if err := flow.Observe(ctx, "live-probe", probe, probe, enabled); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 {
		t.Fatal("replayed same decision")
	}
	t.Logf("ON: one Enter, explicit Codex receipt, no expansion, replay suppressed; fingerprint=%s", fingerprint)
}

type liveApprovalDriver struct {
	term        *terminal
	snapshot    sessionruntime.NativeSnapshot
	fingerprint string
	calls       int
}

func (p *liveApprovalDriver) NativeScreen(domain.SessionID) (sessionruntime.NativeSnapshot, bool) {
	return p.snapshot, true
}
func (p *liveApprovalDriver) NativeScreenUpdates() <-chan domain.SessionID { return nil }
func (p *liveApprovalDriver) NativeControl(ctx context.Context, _ domain.SessionID, r sessionruntime.NativeRequest) (sessionruntime.NativeSnapshot, error) {
	if r.Key != "approve_once" || r.ExpectedHash != p.snapshot.Hash || r.ExpectedProviderSessionID != p.snapshot.ProviderSessionID || r.ExpectedGeneration != p.snapshot.Generation {
		return sessionruntime.NativeSnapshot{}, fmt.Errorf("unexpected unfenced native input")
	}
	p.calls++
	if err := claimOnce("../../.cache/approval-probe", p.term.identity, p.fingerprint); err != nil {
		return sessionruntime.NativeSnapshot{}, err
	}
	err := nativeapproval.AcceptCodexCommandOnce(ctx, p.term, p.fingerprint)
	return sessionruntime.NativeSnapshot{}, err
}
