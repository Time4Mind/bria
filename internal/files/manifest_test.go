package files_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"bria/internal/files"
)

func TestDeliveryManifestPersistsReceiptsAndRetriesOnlyUnconfirmed(t *testing.T) {
	manifest, err := files.NewDeliveryManifest("final-42", []files.Link{{Path: "/tmp/a.txt"}, {Path: "/tmp/b.txt"}})
	if err != nil {
		t.Fatalf("new manifest: %v", err)
	}
	firstID := manifest.Files[0].FileID
	secondID := manifest.Files[1].FileID
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("unstable file ids: %#v", manifest.Files)
	}
	if err := manifest.MarkAttempt(firstID); err != nil {
		t.Fatal(err)
	}
	if err := manifest.MarkConfirmed(firstID, "receipt-1"); err != nil {
		t.Fatal(err)
	}
	if err := manifest.MarkAttempt(secondID); err != nil {
		t.Fatal(err)
	}
	if err := manifest.MarkUnconfirmed(secondID, "transport_unknown"); err != nil {
		t.Fatal(err)
	}

	retryable := manifest.Retryable()
	if len(retryable) != 1 || retryable[0].FileID != secondID {
		t.Fatalf("retryable = %#v, want only second file", retryable)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var restored files.DeliveryManifest
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatalf("validate restored manifest: %v", err)
	}
	if !reflect.DeepEqual(restored, manifest) {
		t.Fatalf("restored = %#v, want %#v", restored, manifest)
	}
}

func TestDeliveryManifestUsesStableIDsAcrossReplay(t *testing.T) {
	links := []files.Link{{Path: "/tmp/a.txt"}, {Path: "/tmp/b.txt"}}
	first, err := files.NewDeliveryManifest("final-42", links)
	if err != nil {
		t.Fatal(err)
	}
	second, err := files.NewDeliveryManifest("final-42", links)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("manifests differ: %#v != %#v", first, second)
	}
}

func TestDeliveryManifestRemainsValidBetweenRetryAttemptAndReceipt(t *testing.T) {
	manifest, err := files.NewDeliveryManifest("final-retry", []files.Link{{Path: "/tmp/retry.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	fileID := manifest.Files[0].FileID
	if err := manifest.MarkAttempt(fileID); err != nil {
		t.Fatal(err)
	}
	if err := manifest.MarkUnconfirmed(fileID, "transport_unknown"); err != nil {
		t.Fatal(err)
	}
	if err := manifest.MarkAttempt(fileID); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest invalid after durable retry intent: %v", err)
	}
	if got := manifest.Files[0].State; got != files.DeliveryPending {
		t.Fatalf("state = %q, want pending", got)
	}
}
