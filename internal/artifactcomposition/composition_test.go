package artifactcomposition_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/artifactcomposition"
	"bria/internal/artifactproduction"
	"bria/internal/domain"
	"bria/internal/telegram"
	"bria/internal/turnprocessing"
)

type senderFunc func(context.Context, telegram.SendDocumentRequest) (telegram.FileReceipt, error)

func (function senderFunc) SendDocument(ctx context.Context, request telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
	return function(ctx, request)
}

func TestExactCorrelatedFinalInvokesArtifactDeliveryAndSignedRetryRoute(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "result.txt")
	if err := os.WriteFile(path, []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls int
	service, err := artifactproduction.Open(senderFunc(func(_ context.Context, request telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
		calls++
		return telegram.FileReceipt{}, telegram.ErrDeliveryUnknown
	}), artifactproduction.Config{ManifestDirectory: filepath.Join(root, "manifests"), RetryDirectory: filepath.Join(root, "retries"), AllowedRoots: []string{work}, MaxFileBytes: 1024, ChatID: 42, RetryKey: []byte("0123456789abcdef0123456789abcdef"), RetryTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	composition, err := artifactcomposition.Open(service)
	if err != nil {
		t.Fatal(err)
	}
	final := turnprocessing.FinalObservation{OperationID: "message-1:final", MessageID: "message-1", SessionID: domain.SessionID("11111111-1111-4111-9111-111111111111"), Text: "[artifact](file://" + path + ")"}
	result, err := composition.DeliverFinal(context.Background(), final)
	if err != nil {
		t.Fatalf("DeliverFinal() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("artifact sends = %d", calls)
	}
	if result.Retry == nil {
		t.Fatal("unconfirmed final exposed no signed retry descriptor")
	}
	if _, err := composition.Retry(context.Background(), result.Retry.Token); err != nil {
		t.Fatalf("Retry(signed descriptor) error = %v", err)
	}
	if err := composition.ProcessFinal(context.Background(), turnprocessing.FinalObservation{OperationID: "message-1:event:1", MessageID: "message-1", SessionID: final.SessionID, Text: final.Text}); err == nil {
		t.Fatal("non-final observation reached artifact delivery")
	}
}
