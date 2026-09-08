package nativereceiptstore_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/nativeacceptance"
	"bria/internal/nativereceiptstore"
)

func TestAtomicReceiptRoundTripAndReadHasNoSideEffects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if _, err := nativereceiptstore.Read(root, "s"); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read created missing receipt directory")
	}
	doc := nativeacceptance.Document{SessionID: "s", Receipts: map[string]string{"m": "unknown"}, TurnIDs: map[string]string{"m": "turn"}}
	if err := nativereceiptstore.Write(root, doc); err != nil {
		t.Fatal(err)
	}
	path, _ := nativereceiptstore.Path(root, "s")
	before, _ := os.Stat(path)
	for i := 0; i < 2; i++ {
		got, err := nativereceiptstore.Read(root, "s")
		if err != nil || !reflect.DeepEqual(got, doc) {
			t.Fatalf("roundtrip = %#v %v", got, err)
		}
	}
	after, _ := os.Stat(path)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || before.Mode().Perm() != 0600 {
		t.Fatal("read altered receipt or leaked sidecar")
	}
	doc.TurnIDs["m"] = ""
	if err := nativereceiptstore.Write(root, doc); err == nil {
		t.Fatal("invalid update committed")
	}
	got, err := nativereceiptstore.Read(root, "s")
	if err != nil || got.TurnIDs["m"] != "turn" {
		t.Fatal("failed update corrupted last receipt")
	}
}

func TestReceiptStoreRejectsUnsafeBindingsAndPermissions(t *testing.T) {
	doc := nativeacceptance.Document{SessionID: "s", Receipts: map[string]string{"m": "unknown"}}
	root := filepath.Join(t.TempDir(), "private")
	if err := nativereceiptstore.Write(root, doc); err != nil {
		t.Fatal(err)
	}
	path, _ := nativereceiptstore.Path(root, "s")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := nativereceiptstore.Read(root, "s"); err == nil {
		t.Fatal("nonprivate receipt read")
	}
	if err := nativereceiptstore.Write(root, doc); err == nil {
		t.Fatal("nonprivate receipt overwritten")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".actual"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".actual", path); err != nil {
		t.Fatal(err)
	}
	if _, err := nativereceiptstore.Read(root, "s"); err == nil {
		t.Fatal("symlink receipt followed")
	}
	if err := nativereceiptstore.Write(root, doc); err == nil {
		t.Fatal("symlink receipt replaced")
	}
	for _, id := range []string{"", ".", "..", "../s", "a/b", "a\\b", "a\x00b"} {
		if _, err := nativereceiptstore.Path(root, id); err == nil {
			t.Fatal("unsafe identity accepted")
		}
	}
}
