package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"bria/internal/config"
)

func TestLoadFileReadsStrictConfiguration(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bria.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	got, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if got.OwnerUserID != 123456789 {
		t.Errorf("OwnerUserID = %d, want decoded configuration", got.OwnerUserID)
	}
}

func TestLoadFileRejectsUnsafeFileTypeAndPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics are unavailable on Windows")
	}

	directory := t.TempDir()
	validPath := filepath.Join(directory, "valid.json")
	if err := os.WriteFile(validPath, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatalf("write valid config fixture: %v", err)
	}
	symlinkPath := filepath.Join(directory, "symlink.json")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}

	unsafeModes := []struct {
		name string
		mode os.FileMode
	}{
		{name: "owner cannot write", mode: 0o400},
		{name: "group writable", mode: 0o620},
		{name: "world writable", mode: 0o602},
	}
	paths := []struct {
		name string
		path string
	}{
		{name: "symlink", path: symlinkPath},
		{name: "directory", path: directory},
	}
	for _, mode := range unsafeModes {
		path := filepath.Join(directory, mode.name+".json")
		if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
			t.Fatalf("write %s config fixture: %v", mode.name, err)
		}
		if err := os.Chmod(path, mode.mode); err != nil {
			t.Fatalf("chmod %s config fixture: %v", mode.name, err)
		}
		paths = append(paths, struct {
			name string
			path string
		}{name: mode.name, path: path})
	}

	for _, test := range paths {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if _, err := config.LoadFile(test.path); err == nil {
				t.Fatalf("LoadFile(%q) error = nil, want unsafe config rejection", test.path)
			}
		})
	}
}

func TestLoadFileAllowsReadOnlyGroupAndWorldAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics are unavailable on Windows")
	}

	path := filepath.Join(t.TempDir(), "bria.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod config fixture: %v", err)
	}
	if _, err := config.LoadFile(path); err != nil {
		t.Fatalf("LoadFile() error = %v, want read-only group/world access accepted", err)
	}
}

func TestLoadFileRejectsConfigPathCollision(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"state", "token", "callback"} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "bria.json")
			document := validConfigJSON
			switch field {
			case "state":
				document = strings.Replace(
					document,
					strconv.Quote("/var/lib/bria/sessions.json"),
					strconv.Quote(path),
					1,
				)
			case "token":
				document = configWithSecretFile(path)
			case "callback":
				document = configWithCallbackKey(path)
			}
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatalf("write config fixture: %v", err)
			}
			if _, err := config.LoadFile(path); err == nil {
				t.Fatal("LoadFile() error = nil, want config path collision rejection")
			}
		})
	}
}

func TestDecodeRejectsTokenAndCallbackKeyPathCollision(t *testing.T) {
	t.Parallel()

	shared := filepath.Join(t.TempDir(), "shared-secret")
	document := configWithSecretFile(shared)
	document = strings.Replace(
		document,
		strconv.Quote("/var/lib/bria/callback.key"),
		strconv.Quote(shared),
		1,
	)
	if _, err := config.Decode(strings.NewReader(document)); err == nil {
		t.Fatal("Decode() error = nil, want token/callback key collision rejection")
	}
}

func TestLoadFileRejectsSymlinkEquivalentStatePath(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "bria.json")
	alias := filepath.Join(directory, "state-alias.json")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatalf("create state alias: %v", err)
	}
	document := strings.Replace(
		validConfigJSON,
		strconv.Quote("/var/lib/bria/sessions.json"),
		strconv.Quote(alias),
		1,
	)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	if _, err := config.LoadFile(path); err == nil {
		t.Fatal("LoadFile() error = nil, want symlink-equivalent state collision rejection")
	}
}

func TestDecodeRejectsSymlinkEquivalentStateAndTokenPaths(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "telegram-token")
	if err := os.WriteFile(tokenPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write token fixture: %v", err)
	}
	stateAlias := filepath.Join(directory, "state-alias")
	if err := os.Symlink(tokenPath, stateAlias); err != nil {
		t.Fatalf("create state alias: %v", err)
	}
	document := configWithSecretFile(tokenPath)
	document = strings.Replace(
		document,
		strconv.Quote("/var/lib/bria/sessions.json"),
		strconv.Quote(stateAlias),
		1,
	)
	if _, err := config.Decode(strings.NewReader(document)); err == nil {
		t.Fatal("Decode() error = nil, want symlink-equivalent state/token collision rejection")
	}
}

func TestDecodeRejectsEquivalentNonexistentPathsBelowSymlinkAncestor(t *testing.T) {
	t.Parallel()

	realRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Fatalf("create root alias: %v", err)
	}
	realFuturePath := filepath.Join(realRoot, "not-created", "shared")
	aliasFuturePath := filepath.Join(aliasRoot, "not-created", "shared")
	document := configWithSecretFile(realFuturePath)
	document = strings.Replace(
		document,
		strconv.Quote("/var/lib/bria/sessions.json"),
		strconv.Quote(aliasFuturePath),
		1,
	)
	if _, err := config.Decode(strings.NewReader(document)); err == nil {
		t.Fatal("Decode() error = nil, want future paths below symlink ancestor treated as equivalent")
	}
}

func TestFileStoreReloadRetainsLastValidConfiguration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "bria.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	validEdit := strings.Replace(validConfigJSON, `"enabled": true`, `"enabled": false`, 1)
	if err := os.WriteFile(path, []byte(validEdit), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := store.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload(valid edit) error = %v", err)
	}
	if after.Revision != before.Revision+1 || after.Config.ProviderEnabled("codex") {
		t.Fatalf("Reload(valid edit) = %#v", after)
	}

	if err := os.WriteFile(path, []byte(`{"owner_user_id": 0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reload(context.Background()); err == nil {
		t.Fatal("Reload(invalid edit) error = nil")
	}
	active, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if active.ProviderEnabled("codex") {
		t.Fatal("invalid edit replaced last valid disabled-provider configuration")
	}
	if store.LastReloadError() == nil {
		t.Fatal("LastReloadError() = nil, want invalid edit observable")
	}
}

func TestFileStoreCASPersistsProviderToggleAndPreservesSecretReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bria.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	committed, err := store.SetProviderEnabled(context.Background(), initial.Revision, "codex", false)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Revision != initial.Revision+1 || committed.Config.ProviderEnabled("codex") {
		t.Fatalf("SetProviderEnabled() = %#v", committed)
	}
	if committed.Config.TelegramToken != initial.Config.TelegramToken || committed.Config.CallbackKey != initial.Config.CallbackKey {
		t.Fatal("provider toggle mutated secret references")
	}
	persisted, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ProviderEnabled("codex") || persisted.TelegramToken != initial.Config.TelegramToken || persisted.CallbackKey != initial.Config.CallbackKey {
		t.Fatalf("persisted configuration = %#v", persisted)
	}
	if _, err := store.SetProviderEnabled(context.Background(), initial.Revision, "codex", true); !errors.Is(err, config.ErrRevisionConflict) {
		t.Fatalf("stale SetProviderEnabled() error = %v, want ErrRevisionConflict", err)
	}
}
