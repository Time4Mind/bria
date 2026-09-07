package recoveryruntime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/recoveryruntime"
	"bria/internal/sessionruntime"
)

const nativeReceiptID = "00000000-0000-4000-8000-000000000077"

func TestNativeReceiptsBothProvidersWithoutProviderProcess(t *testing.T) {
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			reader, err := recoveryruntime.NewNative(root, provider)
			if err != nil {
				t.Fatal(err)
			}
			request := nativeRequest(provider)
			if _, err := reader.ReadAcceptedTurns(context.Background(), request); !errors.Is(err, recoveryruntime.ErrUnavailable) {
				t.Fatalf("missing treated as success: %v", err)
			}
			writeNative(t, root, `{"session_id":"`+nativeReceiptID+`","receipts":{"a":"completed","b":"unknown","c":"failed"}}`)
			got, err := reader.ReadAcceptedTurns(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Turns) != 3 || got.Turns[0].Outcome != sessionruntime.AcceptedTurnCompleted || got.Turns[1].Outcome != sessionruntime.AcceptedTurnUnknown || got.Turns[2].Outcome != sessionruntime.AcceptedTurnFailed {
				t.Fatalf("got=%#v", got)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := reader.ReadAcceptedTurns(ctx, request); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func nativeRequest(provider domain.Provider) sessionruntime.AcceptedTurnReadRequest {
	return sessionruntime.AcceptedTurnReadRequest{SessionID: "logical", Provider: provider, Workdir: "/work", Binding: domain.ProviderBinding{Provider: provider, SessionID: nativeReceiptID, Generation: 1}}
}
func writeNative(t *testing.T, root, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, nativeReceiptID+".json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNativeReceiptsRejectUnverifiableDocuments(t *testing.T) {
	cases := []string{
		`{"session_id":"foreign","receipts":{}}`,
		`{"session_id":"` + nativeReceiptID + `","receipts":{"a":"sent"}}`,
		`{"session_id":"` + nativeReceiptID + `","receipts":{"a":"failed","a":"completed"}}`,
		`{"session_id":"foreign","session_id":"` + nativeReceiptID + `","receipts":{}}`,
		`{"session_id":"` + nativeReceiptID + `","receipts":null}`,
		`{"session_id":"` + nativeReceiptID + `"}`,
		strings.Repeat("x", (1<<20)+1),
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	reader, err := recoveryruntime.NewNative(root, domain.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	for i, data := range cases {
		writeNative(t, root, data)
		if _, err := reader.ReadAcceptedTurns(context.Background(), nativeRequest(domain.ProviderCodex)); !errors.Is(err, recoveryruntime.ErrUnavailable) {
			t.Fatalf("case%d: %v", i, err)
		}
	}
	writeNative(t, root, `{"session_id":"`+nativeReceiptID+`","receipts":{}}`)
	wrong := nativeRequest(domain.ProviderClaude)
	if _, err := reader.ReadAcceptedTurns(context.Background(), wrong); !errors.Is(err, recoveryruntime.ErrUnavailable) {
		t.Fatal(err)
	}
	path := filepath.Join(root, nativeReceiptID+".json")
	if err := os.Rename(path, path+".actual"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".actual", path); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadAcceptedTurns(context.Background(), nativeRequest(domain.ProviderCodex)); !errors.Is(err, recoveryruntime.ErrUnavailable) {
		t.Fatal(err)
	}
}
