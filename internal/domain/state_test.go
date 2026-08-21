package domain_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func fixtureState(t *testing.T) *domain.State {
	t.Helper()
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "alpha", Name: "Alpha", Status: domain.NodeOnline, BootID: "boot-a"},
		{ID: "beta", Name: "Beta", Status: domain.NodeOnline, BootID: "boot-b"},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatalf("add node: %v", err)
		}
	}
	if err := state.SetNodeAccess(1, domain.RoleOwner, "alpha", "beta"); err != nil {
		t.Fatalf("set owner access: %v", err)
	}
	if err := state.SetNodeAccess(2, domain.RoleMember, "alpha"); err != nil {
		t.Fatalf("set member access: %v", err)
	}
	return state
}

func addSession(t *testing.T, state *domain.State, id, node string, owner domain.UserID, created time.Time) domain.SessionRef {
	t.Helper()
	session := domain.Session{
		ID:                domain.SessionID(id),
		NodeID:            domain.NodeID(node),
		OwnerID:           owner,
		Name:              id,
		Workdir:           "/srv/" + id,
		Backend:           "codex",
		ProviderSessionID: "provider-" + id,
		State:             domain.SessionActive,
		CreatedAt:         created,
		LiveSinceAt:       created,
		LastEventAt:       created,
	}
	if err := state.AddSession(session); err != nil {
		t.Fatalf("add session: %v", err)
	}
	return session.Ref()
}

func TestSessionsUseNodeQualifiedIdentity(t *testing.T) {
	state := fixtureState(t)
	now := time.Unix(100, 0).UTC()
	addSession(t, state, "local-1", "alpha", 1, now)
	addSession(t, state, "local-1", "beta", 1, now)

	if got := len(state.Sessions); got != 2 {
		t.Fatalf("session count = %d, want 2", got)
	}
}

func TestSelectNodeChoosesItsMostRecentlyActiveLiveSession(t *testing.T) {
	state := fixtureState(t)
	started := time.Unix(100, 0).UTC()
	addSession(t, state, "alpha-live", "alpha", 1, started)
	older := addSession(t, state, "beta-older", "beta", 1, started.Add(time.Second))
	addSession(t, state, "beta-newer", "beta", 1, started.Add(2*time.Second))
	if err := state.RecordSessionActivity(1, older, started.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	tracked := state.Sessions[older.Key()]
	if !tracked.UserRequestTracked || !tracked.UserRequestSeen {
		t.Fatalf("sent request was not tracked: %#v", tracked)
	}
	if got := state.Navigation.ActiveSessionByUserNode[1]["beta"]; got != "" {
		t.Fatalf("beta session selected before entering node: %q", got)
	}
	if err := state.SelectNode(1, "beta", started.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := state.Navigation.ActiveNodeByUser[1]; got != "beta" {
		t.Fatalf("active node=%q", got)
	}
	if got := state.Navigation.ActiveSessionByUserNode[1]["beta"]; got != older.SessionID {
		t.Fatalf("active beta session=%q, want %q", got, older.SessionID)
	}
}

func TestNodeACLAndPrivateSessionsAreEnforced(t *testing.T) {
	state := fixtureState(t)
	now := time.Unix(100, 0).UTC()
	alpha := addSession(t, state, "a", "alpha", 1, now)
	beta := addSession(t, state, "b", "beta", 1, now.Add(time.Second))

	if state.CanViewSession(2, alpha) {
		t.Fatal("private owner session unexpectedly visible")
	}
	if state.CanViewSession(2, beta) {
		t.Fatal("session on forbidden node unexpectedly visible")
	}
	if got := state.VisibleNodes(2); len(got) != 1 || got[0].ID != "alpha" {
		t.Fatalf("visible nodes = %#v, want alpha only", got)
	}
}

func TestShareSelectsViewOrControlAndCannotBypassNodeACL(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "shared", "alpha", 1, time.Unix(100, 0).UTC())

	if err := state.ShareSession(1, ref, 2, domain.ShareView); err != nil {
		t.Fatalf("share view: %v", err)
	}
	if !state.CanViewSession(2, ref) || state.CanControlSession(2, ref) {
		t.Fatal("view grant has wrong permissions")
	}
	if err := state.ShareSession(1, ref, 2, domain.ShareControl); err != nil {
		t.Fatalf("share control: %v", err)
	}
	if !state.CanControlSession(2, ref) {
		t.Fatal("control grant did not allow control")
	}
	if !state.CanPerformSessionAction(2, ref, domain.ActionSendInput) {
		t.Fatal("control grant did not allow input")
	}
	if state.CanPerformSessionAction(2, ref, domain.ActionArchive) {
		t.Fatal("control grant unexpectedly allowed owner lifecycle action")
	}

	betaRef := addSession(t, state, "hidden", "beta", 1, time.Unix(101, 0).UTC())
	err := state.ShareSession(1, betaRef, 2, domain.ShareView)
	if !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("share to forbidden node error = %v, want access denied", err)
	}
}

func TestNodeRebootRecoversSessionsBeforeIdleDeadline(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "live", "alpha", 1, time.Unix(100, 0).UTC())

	plan, err := state.ObserveNodeBoot("alpha", "boot-a", time.Unix(200, 0).UTC())
	if err != nil || len(plan.Recover)+len(plan.Archived) != 0 {
		t.Fatalf("same boot = (%#v, %v), want no action", plan, err)
	}
	plan, err = state.ObserveNodeBoot("alpha", "boot-next", time.Unix(300, 0).UTC())
	if err != nil || len(plan.Recover) != 1 || len(plan.Archived) != 0 {
		t.Fatalf("new boot = (%#v, %v), want one recovery", plan, err)
	}
	session := state.Sessions[ref.Key()]
	if session.State != domain.SessionLive ||
		session.RuntimePhase != domain.RuntimeDegraded ||
		!session.ResumePending {
		t.Fatalf("session after reboot = %#v", session)
	}
	if err := state.CompleteBootRecovery(ref, time.Unix(301, 0).UTC()); err != nil {
		t.Fatalf("complete recovery: %v", err)
	}
	session = state.Sessions[ref.Key()]
	if session.State != domain.SessionLive ||
		session.RuntimePhase != domain.RuntimeIdle ||
		session.ResumePending ||
		!session.LastEventAt.Equal(time.Unix(100, 0).UTC()) ||
		!session.LiveSinceAt.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("recovered session reset idle clock: %#v", session)
	}
}

func TestSameBootReemitsInterruptedRecovery(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "interrupted", "alpha", 1, time.Unix(100, 0).UTC())
	if _, err := state.ObserveNodeBoot("alpha", "boot-next", time.Unix(300, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	plan, err := state.ObserveNodeBoot("alpha", "boot-next", time.Unix(301, 0).UTC())
	if err != nil || len(plan.Recover) != 1 || plan.Recover[0] != ref {
		t.Fatalf("same-boot recovery plan = (%#v, %v)", plan, err)
	}
}

func TestNodeRebootArchivesSessionsWhoseIdleDeadlinePassed(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "expired", "alpha", 1, time.Unix(100, 0).UTC())
	plan, err := state.ObserveNodeBoot("alpha", "boot-next", time.Unix(100, 0).UTC().Add(6*time.Hour))
	if err != nil || len(plan.Archived) != 1 || len(plan.Recover) != 0 {
		t.Fatalf("plan = (%#v, %v)", plan, err)
	}
	session := state.Sessions[ref.Key()]
	if session.State != domain.SessionArchived || session.ArchiveReason != domain.ArchiveIdle {
		t.Fatalf("expired session = %#v", session)
	}
}

func TestNodeRebootAlwaysRecoversUnlimitedSessions(t *testing.T) {
	state := fixtureState(t)
	preferences := domain.DefaultUserPreferences()
	preferences.IdleArchiveHours = 0
	if err := state.SetPreferences(1, preferences); err != nil {
		t.Fatal(err)
	}
	ref := addSession(t, state, "unlimited", "alpha", 1, time.Unix(100, 0).UTC())
	plan, err := state.ObserveNodeBoot("alpha", "boot-next", time.Unix(100, 0).UTC().Add(365*24*time.Hour))
	if err != nil || len(plan.Recover) != 1 || plan.Recover[0] != ref {
		t.Fatalf("unlimited recovery plan = (%#v, %v)", plan, err)
	}
}

func TestUnavailableSessionOnOnlineNodeIsArchivedNotLost(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "ended", "alpha", 1, time.Unix(100, 0).UTC())
	plan, err := state.ObserveNodeBoot("alpha", "boot-next", time.Unix(300, 0).UTC())
	if err != nil || len(plan.Recover) != 1 {
		t.Fatalf("recovery plan = (%#v, %v)", plan, err)
	}
	failedAt := time.Unix(301, 0).UTC()
	if err := state.FailBootRecovery(ref, failedAt); err != nil {
		t.Fatal(err)
	}
	session := state.Sessions[ref.Key()]
	if session.State != domain.SessionArchived ||
		session.ArchiveReason != domain.ArchiveResumeFailed ||
		!session.ArchivedAt.Equal(failedAt) {
		t.Fatalf("failed resume session = %#v", session)
	}
}

func TestMissingProviderBindingAtBootIsArchivedNotLost(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "unbound", "alpha", 1, time.Unix(100, 0).UTC())
	session := state.Sessions[ref.Key()]
	session.ProviderSessionID = ""
	state.Sessions[ref.Key()] = session
	plan, err := state.ObserveNodeBoot("alpha", "boot-next", time.Unix(300, 0).UTC())
	if err != nil || len(plan.Archived) != 1 || len(plan.Recover) != 0 {
		t.Fatalf("unbound plan = (%#v, %v)", plan, err)
	}
	session = state.Sessions[ref.Key()]
	if session.State != domain.SessionArchived || session.ArchiveReason != domain.ArchiveResumeFailed {
		t.Fatalf("unbound session = %#v", session)
	}
}

func TestLiveAndArchiveOrderingMatchesCCBotContract(t *testing.T) {
	state := fixtureState(t)
	older := addSession(t, state, "older", "alpha", 1, time.Unix(10, 0).UTC())
	newer := addSession(t, state, "newer", "beta", 1, time.Unix(20, 0).UTC())

	live := state.VisibleSessions(1, true)
	if live[0].Ref() != older || live[1].Ref() != newer {
		t.Fatalf("live order = %#v", live)
	}
	if _, err := state.ObserveNodeBoot("alpha", "boot-next", time.Unix(10, 0).UTC().Add(6*time.Hour)); err != nil {
		t.Fatalf("archive alpha: %v", err)
	}
	if _, err := state.ObserveNodeBoot("beta", "boot-next", time.Unix(20, 0).UTC().Add(7*time.Hour)); err != nil {
		t.Fatalf("archive beta: %v", err)
	}
	archived := state.VisibleSessions(1, false)
	if archived[0].Ref() != newer || archived[1].Ref() != older {
		t.Fatalf("archive order = %#v", archived)
	}
}

func TestPreferenceChoicesAreClosedAndIncludeUnlimited(t *testing.T) {
	defaults := domain.DefaultUserPreferences()
	if defaults.IdleArchiveHours != 6 || defaults.ArchiveRetentionDays != 14 {
		t.Fatalf("unexpected defaults: %#v", defaults)
	}
	for _, preferences := range []domain.UserPreferences{
		defaults,
		{SessionView: domain.ViewAllHosts, IdleArchiveHours: 0, ArchiveRetentionDays: 0, ArchiveExpiryAction: domain.ArchiveRemoveAll},
		{SessionView: domain.ViewHostFirst, IdleArchiveHours: 24, ArchiveRetentionDays: 30, ArchiveExpiryAction: domain.ArchiveRemoveRecord},
	} {
		if err := preferences.Validate(); err != nil {
			t.Fatalf("valid preferences rejected: %v", err)
		}
	}
	invalid := defaults
	invalid.IdleArchiveHours = 48
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid idle archive setting accepted")
	}
}

func TestLanguagePreferenceSupportsAutoAndTelegramInference(t *testing.T) {
	defaults := domain.DefaultUserPreferences()
	if defaults.Language != domain.LanguageAuto ||
		defaults.EffectiveLanguage() != domain.LanguageEnglish {
		t.Fatalf("language defaults=%#v", defaults)
	}
	for code, want := range map[string]domain.Language{
		"ru": domain.LanguageRussian, "ru-RU": domain.LanguageRussian,
		"zh-CN": domain.LanguageChinese, "en-US": domain.LanguageEnglish,
		"": domain.LanguageEnglish,
	} {
		if got := domain.LanguageFromTelegram(code); got != want {
			t.Errorf("LanguageFromTelegram(%q)=%q, want %q", code, got, want)
		}
	}
	invalid := defaults
	invalid.Language = "de"
	if err := invalid.Validate(); err == nil {
		t.Fatal("unsupported language accepted")
	}
}

func TestLegacyPreferenceJSONWithoutLanguageRemainsValid(t *testing.T) {
	var preferences domain.UserPreferences
	if err := json.Unmarshal([]byte(`{
		"session_view":"host_first",
		"idle_archive_hours":6,
		"archive_retention_days":14,
		"archive_expiry_action":"remove_record"
	}`), &preferences); err != nil {
		t.Fatal(err)
	}
	if preferences.Language != domain.LanguageAuto || preferences.Validate() != nil {
		t.Fatalf("legacy preferences=%#v", preferences)
	}
	state := domain.NewState()
	state.Preferences[1] = preferences
	if got := state.Clone().Preferences[1].ArchiveExpiryAction; got != domain.ArchiveRemoveAll {
		t.Fatalf("legacy expiry action after normalization=%q, want %q", got, domain.ArchiveRemoveAll)
	}
}
