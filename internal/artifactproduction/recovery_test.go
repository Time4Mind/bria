package artifactproduction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/telegram"
)

type recoverySender struct {
	sends   int
	succeed bool
}

func (sender *recoverySender) SendDocument(_ context.Context, request telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
	sender.sends++
	if !sender.succeed {
		return telegram.FileReceipt{}, telegram.ErrDeliveryUnknown
	}
	return telegram.FileReceipt{
		MessageID: 501, ChatID: request.ChatID, FileID: "recovered-file", FileUniqueID: "recovered-unique",
	}, nil
}

func TestRecoverClaimedRotatesCrashBeforeAttemptOnlyWhenManifestFenceIsUnchanged(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	state := filepath.Join(root, "state")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "result.txt")
	if err := os.WriteFile(path, []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	sender := &recoverySender{}
	service, err := Open(sender, Config{
		ManifestDirectory: filepath.Join(state, "manifests"), RetryDirectory: filepath.Join(state, "retry"),
		AllowedRoots: []string{work}, MaxFileBytes: 1024, ChatID: 42,
		RetryKey: []byte("0123456789abcdef0123456789abcdef"), RetryTTL: time.Hour,
		Now: func() time.Time { return time.Unix(2_000_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	initial, err := service.DeliverFinal(context.Background(), "final-crash-boundary", "[result](file://"+path+")")
	if err != nil || initial.Retry == nil {
		t.Fatalf("DeliverFinal() = %#v, %v", initial, err)
	}
	if _, err := service.retries.resolve(context.Background(), initial.Retry.Token, func(finalID string) (string, error) {
		return service.manifestFence(context.Background(), finalID)
	}); err != nil {
		t.Fatalf("simulate durable claim before crash: %v", err)
	}
	recovered, err := service.RecoverClaimed(context.Background())
	if err != nil {
		t.Fatalf("RecoverClaimed() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].Token == initial.Retry.Token {
		t.Fatalf("recovered descriptors = %#v", recovered)
	}
	if _, err := service.Retry(context.Background(), initial.Retry.Token); !errors.Is(err, ErrInvalidRetry) {
		t.Fatalf("old claimed token error = %v", err)
	}
	sender.succeed = true
	beforeRetry := sender.sends
	result, err := service.Retry(context.Background(), recovered[0].Token)
	if err != nil {
		t.Fatalf("Retry(recovered) error = %v", err)
	}
	if sender.sends != beforeRetry+1 || result.Summary.Confirmed != 1 {
		t.Fatalf("recovered retry sends=%d result=%#v", sender.sends-beforeRetry, result)
	}
	afterReceipt := sender.sends
	completed, err := service.RecoverClaimedResults(context.Background())
	if err != nil || len(completed) != 1 || completed[0].Retry != nil || completed[0].Summary.Confirmed != 1 {
		t.Fatalf("resolved cross-store recovery = %#v, %v", completed, err)
	}
	if sender.sends != afterReceipt {
		t.Fatalf("resolved cross-store recovery resent confirmed artifact: %d", sender.sends-afterReceipt)
	}
}

func TestRecoverClaimedTurnsAdvancedAttemptIntoNewManualDecisionWithoutSending(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	state := filepath.Join(root, "state")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "result.txt")
	if err := os.WriteFile(path, []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := Open(&recoverySender{}, Config{
		ManifestDirectory: filepath.Join(state, "manifests"), RetryDirectory: filepath.Join(state, "retry"),
		AllowedRoots: []string{work}, MaxFileBytes: 1024, ChatID: 42,
		RetryKey: []byte("0123456789abcdef0123456789abcdef"), RetryTTL: time.Hour,
		Now: func() time.Time { return time.Unix(2_000_000_000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.DeliverFinal(context.Background(), "final-advanced", "[result](file://"+path+")")
	if err != nil || initial.Retry == nil {
		t.Fatalf("DeliverFinal() = %#v, %v", initial, err)
	}
	if _, err := service.retries.resolve(context.Background(), initial.Retry.Token, func(finalID string) (string, error) {
		return service.manifestFence(context.Background(), finalID)
	}); err != nil {
		t.Fatal(err)
	}
	manifest, found, err := service.manifestStore.Load(context.Background(), "final-advanced")
	if err != nil || !found {
		t.Fatalf("Load() = %v, %v", found, err)
	}
	if err := manifest.MarkAttempt(manifest.Files[0].FileID); err != nil {
		t.Fatal(err)
	}
	if err := service.manifestStore.Save(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.RecoverClaimedResults(context.Background())
	if err != nil {
		t.Fatalf("RecoverClaimed() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].FinalID != "final-advanced" || recovered[0].Retry == nil || recovered[0].Retry.Token == initial.Retry.Token ||
		recovered[0].Summary.Unconfirmed != 1 {
		t.Fatalf("advanced attempt recovery = %#v", recovered)
	}
	if sender := service.delivery.Transport.(telegramTransport).sender.(*recoverySender); sender.sends != 1 {
		t.Fatalf("recovery performed an automatic send: %d", sender.sends)
	}
}

func TestRecoverClaimedCompletesReceiptConfirmedBeforeCrashWithoutRetry(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	work, state := filepath.Join(root, "work"), filepath.Join(root, "state")
	_ = os.Mkdir(work, 0o700)
	_ = os.Mkdir(state, 0o700)
	path := filepath.Join(work, "result.txt")
	_ = os.WriteFile(path, []byte("result"), 0o600)
	sender := &recoverySender{}
	service, err := Open(sender, Config{ManifestDirectory: filepath.Join(state, "manifests"), RetryDirectory: filepath.Join(state, "retry"), AllowedRoots: []string{work}, MaxFileBytes: 1024, ChatID: 42, RetryKey: []byte("0123456789abcdef0123456789abcdef"), RetryTTL: time.Hour, Now: func() time.Time { return time.Unix(2_000_000_000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := service.DeliverFinal(context.Background(), "final-confirmed-crash", "[result](file://"+path+")")
	if _, err := service.retries.resolve(context.Background(), initial.Retry.Token, func(finalID string) (string, error) { return service.manifestFence(context.Background(), finalID) }); err != nil {
		t.Fatal(err)
	}
	manifest, _, _ := service.manifestStore.Load(context.Background(), "final-confirmed-crash")
	_ = manifest.MarkAttempt(manifest.Files[0].FileID)
	_ = manifest.MarkConfirmed(manifest.Files[0].FileID, "telegram:501:0123456789abcdef0123456789abcdef")
	if err := service.manifestStore.Save(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.RecoverClaimedResults(context.Background())
	if err != nil || len(recovered) != 1 || recovered[0].Retry != nil || recovered[0].Summary.Confirmed != 1 {
		t.Fatalf("confirmed crash recovery = %#v, %v", recovered, err)
	}
	if sender.sends != 1 {
		t.Fatalf("confirmed recovery resent artifact: %d", sender.sends)
	}
}

func TestRecoverIssuedResultAfterCrossStoreCrashWithoutSending(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	work, state := filepath.Join(root, "work"), filepath.Join(root, "state")
	_ = os.Mkdir(work, 0o700)
	_ = os.Mkdir(state, 0o700)
	path := filepath.Join(work, "result.txt")
	_ = os.WriteFile(path, []byte("result"), 0o600)
	sender := &recoverySender{}
	service, err := Open(sender, Config{ManifestDirectory: filepath.Join(state, "manifests"), RetryDirectory: filepath.Join(state, "retry"), AllowedRoots: []string{work}, MaxFileBytes: 1024, ChatID: 42, RetryKey: []byte("0123456789abcdef0123456789abcdef"), RetryTTL: time.Hour, Now: func() time.Time { return time.Unix(2_000_000_000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.DeliverFinal(context.Background(), "final-issued-crash", "[result](file://"+path+")")
	if err != nil || initial.Retry == nil {
		t.Fatalf("DeliverFinal() = %#v, %v", initial, err)
	}
	retried, err := service.Retry(context.Background(), initial.Retry.Token)
	if err != nil || retried.Retry == nil || retried.Retry.Token == initial.Retry.Token {
		t.Fatalf("Retry() = %#v, %v", retried, err)
	}
	beforeRecovery := sender.sends
	recovered, err := service.RecoverClaimedResults(context.Background())
	if err != nil || len(recovered) != 1 || recovered[0].Retry == nil || recovered[0].Retry.Token != retried.Retry.Token {
		t.Fatalf("issued cross-store recovery = %#v, %v", recovered, err)
	}
	if sender.sends != beforeRecovery {
		t.Fatalf("issued cross-store recovery sent artifact: before=%d after=%d", beforeRecovery, sender.sends)
	}
}
