package promptpreprocessbinding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"bria/internal/domain"
)

func TestFileBindingStorePersistsPrivateVersionedBindingsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "satellites.json")
	store, err := OpenFileBindingStore(path)
	if err != nil {
		t.Fatal(err)
	}
	records := []Binding{
		{Key: BindingKey{Mode: ModeShared}, Desired: DesiredActive, ProviderSessionID: "shared-thread"},
		{Key: BindingKey{Mode: ModePerSession, SessionID: "main-a"}, Desired: DesiredArchived, ProviderSessionID: "personal-thread"},
	}
	for _, record := range records {
		if err := store.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("binding file = %#v, %v", info, err)
	}
	reopened, err := OpenFileBindingStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range records {
		got, found, loadErr := reopened.Load(context.Background(), want.Key)
		if loadErr != nil || !found || got != want {
			t.Fatalf("reopened binding = %#v, %t, %v; want %#v", got, found, loadErr, want)
		}
	}
	if err := reopened.Delete(context.Background(), records[1].Key); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(context.Background(), records[1].Key); err != nil || found {
		t.Fatalf("delete not visible across store instances: found=%t err=%v", found, err)
	}
}

func TestOpenFileBindingStoreRejectsUnsafeOrCorruptFiles(t *testing.T) {
	root := t.TempDir()
	validEmpty := `{"version":1,"bindings":[]}`
	tests := []struct {
		name    string
		prepare func(string) string
	}{
		{"symlink", func(path string) string {
			target := filepath.Join(root, "target.json")
			if err := os.WriteFile(target, []byte(validEmpty), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"directory", func(path string) string {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"permissions", func(path string) string {
			if err := os.WriteFile(path, []byte(validEmpty), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"corrupt", func(path string) string {
			if err := os.WriteFile(path, []byte(`{"version":1,"bindings":`), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"unknown-version", func(path string) string {
			if err := os.WriteFile(path, []byte(`{"version":2,"bindings":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"unknown-field", func(path string) string {
			if err := os.WriteFile(path, []byte(`{"version":1,"bindings":[],"extra":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"duplicate", func(path string) string {
			document := `{"version":1,"bindings":[{"mode":"per_session","session_id":"main","desired":"active"},{"mode":"per_session","session_id":"main","desired":"archived"}]}`
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"oversized", func(path string) string {
			if err := os.WriteFile(path, make([]byte, maxBindingDocumentBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, fmt.Sprintf("unsafe-%d", index))
			if store, err := OpenFileBindingStore(test.prepare(path)); err == nil || store != nil {
				t.Fatalf("unsafe store opened: %#v, %v", store, err)
			}
		})
	}
}

func TestFileBindingStoreSerializesMultipleInstancesWithoutLostBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "satellites.json")
	one, err := OpenFileBindingStore(path)
	if err != nil {
		t.Fatal(err)
	}
	two, err := OpenFileBindingStore(path)
	if err != nil {
		t.Fatal(err)
	}
	const count = 64
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			store := one
			if index%2 == 1 {
				store = two
			}
			record := Binding{
				Key:     BindingKey{Mode: ModePerSession, SessionID: domainSessionID(index)},
				Desired: DesiredActive, ProviderSessionID: fmt.Sprintf("thread-%d", index),
			}
			if err := store.Save(context.Background(), record); err != nil {
				t.Errorf("save %d: %v", index, err)
			}
		}(index)
	}
	wait.Wait()
	reopened, err := OpenFileBindingStore(path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := reopened.List(context.Background())
	if err != nil || len(records) != count {
		t.Fatalf("records = %d, %v", len(records), err)
	}
	for index := range count {
		key := BindingKey{Mode: ModePerSession, SessionID: domainSessionID(index)}
		if _, found, err := reopened.Load(context.Background(), key); err != nil || !found {
			t.Fatalf("binding %d missing: found=%t err=%v", index, found, err)
		}
	}
}

func domainSessionID(index int) domain.SessionID {
	return domain.SessionID(fmt.Sprintf("main-%d", index))
}
