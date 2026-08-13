package callbacktoken_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestTokensAreCompactActorAndActionScoped(t *testing.T) {
	codec, err := callbacktoken.New([]byte(strings.Repeat("k", callbacktoken.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	first, err := codec.Session(1, telegramui.ActionSelectSession, domain.SessionRef{NodeID: "node", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := codec.Session(2, telegramui.ActionSelectSession, domain.SessionRef{NodeID: "node", SessionID: "session"})
	otherAction, _ := codec.Session(1, telegramui.ActionArchive, domain.SessionRef{NodeID: "node", SessionID: "session"})
	if first == second || first == otherAction {
		t.Fatalf("tokens are not scoped: %q %q %q", first, second, otherAction)
	}
	encoded, err := (telegramui.Callback{Action: telegramui.ActionSelectSession, Token: first}).Encode()
	if err != nil || len(encoded) > telegramui.MaxCallbackBytes {
		t.Fatalf("encoded callback = %q, %v", encoded, err)
	}
	if strings.Contains(string(first), "node") || strings.Contains(string(first), "session") {
		t.Fatalf("token disclosed entity identity: %q", first)
	}
}

func TestLoadFileRequiresExactPrivateKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "callback.key")
	if err := os.WriteFile(path, []byte(strings.Repeat("s", callbacktoken.KeyBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := callbacktoken.LoadFile(path); err != nil {
		t.Fatalf("load private callback key: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := callbacktoken.LoadFile(path); err == nil {
			t.Fatal("world-readable callback key accepted")
		}
	}
}

func TestResolveOnlyScansAuthorizedCandidates(t *testing.T) {
	codec, err := callbacktoken.New([]byte(strings.Repeat("x", callbacktoken.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	wanted := domain.SessionRef{NodeID: "allowed", SessionID: "s1"}
	token, err := codec.Session(42, telegramui.ActionSelectSession, wanted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.ResolveSession(42, telegramui.ActionSelectSession, token, []domain.SessionRef{{NodeID: "other", SessionID: "s2"}}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unauthorized candidate resolution error=%v", err)
	}
	resolved, err := codec.ResolveSession(42, telegramui.ActionSelectSession, token, []domain.SessionRef{wanted})
	if err != nil || resolved != wanted {
		t.Fatalf("resolved=(%#v, %v)", resolved, err)
	}
}

func TestSessionPageTokenBindsPageWithoutDisclosingIt(t *testing.T) {
	codec, err := callbacktoken.New([]byte(strings.Repeat("p", callbacktoken.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	token, err := codec.Page(7, telegramui.ActionPageNext, ref, 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(token), "session") || strings.Contains(string(token), "node") {
		t.Fatalf("page token disclosed state: %q", token)
	}
	candidates := []callbacktoken.SessionPage{
		{Session: ref, Page: 2},
		{Session: ref, Page: 3},
	}
	resolved, err := codec.ResolvePage(7, telegramui.ActionPageNext, token, candidates)
	if err != nil || resolved.Session != ref || resolved.Page != 3 {
		t.Fatalf("resolved=(%#v, %v)", resolved, err)
	}
	if _, err := codec.ResolvePage(
		7, telegramui.ActionPagePrevious, token, candidates,
	); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-action page token error=%v", err)
	}
}

func TestArchiveTokenBindsActorActionSessionAndListPage(t *testing.T) {
	codec, err := callbacktoken.New([]byte(strings.Repeat("a", callbacktoken.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "node", SessionID: "archived-session"}
	token, err := codec.Archive(7, telegramui.ActionSelectArchive, ref, 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(token), "node") || strings.Contains(string(token), "archived") {
		t.Fatalf("archive token disclosed state: %q", token)
	}
	candidates := []callbacktoken.ArchiveSelection{
		{Session: ref, ListPage: 2},
		{Session: ref, ListPage: 3},
	}
	resolved, err := codec.ResolveArchive(
		7, telegramui.ActionSelectArchive, token, candidates,
	)
	if err != nil || resolved.Session != ref || resolved.ListPage != 3 {
		t.Fatalf("resolved=(%#v, %v)", resolved, err)
	}
	if _, err := codec.ResolveArchive(
		8, telegramui.ActionSelectArchive, token, candidates,
	); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-actor archive token error=%v", err)
	}
	if _, err := codec.ResolveArchive(
		7, telegramui.ActionArchiveBack, token, candidates,
	); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-action archive token error=%v", err)
	}
}
