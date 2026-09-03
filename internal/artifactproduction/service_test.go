package artifactproduction_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"bria/internal/artifactproduction"
	"bria/internal/telegram"
)

type documentSenderFunc func(context.Context, telegram.SendDocumentRequest) (telegram.FileReceipt, error)

func (function documentSenderFunc) SendDocument(ctx context.Context, request telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
	return function(ctx, request)
}

func TestDeliveryProducesDurableSignedRetryAndNeverResendsConfirmedFiles(t *testing.T) {
	root := canonicalTempDir(t)
	work := filepath.Join(root, "work")
	mustMkdir(t, work)
	first := filepath.Join(work, "first.txt")
	second := filepath.Join(work, "second.txt")
	mustWrite(t, first, "first-content")
	mustWrite(t, second, "second-content")
	state := filepath.Join(root, "state")
	mustMkdir(t, state)

	requests := make([]telegram.SendDocumentRequest, 0, 2)
	sender := documentSenderFunc(func(_ context.Context, request telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
		requests = append(requests, request)
		if request.FileName == "second.txt" {
			return telegram.FileReceipt{}, telegram.ErrDeliveryUnknown
		}
		return telegram.FileReceipt{MessageID: 71, ChatID: request.ChatID, FileID: "remote-first", FileUniqueID: "unique-first"}, nil
	})
	service := openService(t, sender, state, []string{work}, time.Unix(2_000_000_000, 0))
	result, err := service.DeliverFinal(context.Background(), "provider-final-1", markdownLinks(first, second))
	if err != nil {
		t.Fatalf("DeliverFinal() error = %v", err)
	}
	if result.Summary.Total != 2 || result.Summary.Confirmed != 1 || result.Summary.Unconfirmed != 1 || !result.Summary.NeedsExplicitRetry {
		t.Fatalf("summary = %#v", result.Summary)
	}
	if result.Retry == nil || len(result.Retry.Token) == 0 || len(result.Retry.Token) > 64 || result.Retry.ExpiresAt.IsZero() {
		t.Fatalf("retry descriptor = %#v", result.Retry)
	}
	if len(requests) != 2 || requests[0].ChatID != 4242 || requests[0].FileName != "first.txt" || string(requests[0].Content) != "first-content" ||
		requests[1].FileName != "second.txt" || string(requests[1].Content) != "second-content" {
		t.Fatalf("Telegram requests = %#v", requests)
	}

	retryNames := make([]string, 0, 1)
	reopened := openService(t, documentSenderFunc(func(_ context.Context, request telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
		retryNames = append(retryNames, request.FileName)
		return telegram.FileReceipt{MessageID: 72, ChatID: request.ChatID, FileID: "remote-second", FileUniqueID: "unique-second"}, nil
	}), state, []string{work}, time.Unix(2_000_000_010, 0))
	retried, err := reopened.Retry(context.Background(), result.Retry.Token)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if !reflect.DeepEqual(retryNames, []string{"second.txt"}) {
		t.Fatalf("retried files = %#v", retryNames)
	}
	if retried.Summary.Confirmed != 2 || retried.Summary.Unconfirmed != 0 || retried.Retry != nil {
		t.Fatalf("retried result = %#v", retried)
	}
	retryNames = nil
	if _, err := reopened.Retry(context.Background(), result.Retry.Token); !errors.Is(err, artifactproduction.ErrInvalidRetry) {
		t.Fatalf("replayed Retry() error = %v", err)
	}
	if len(retryNames) != 0 {
		t.Fatalf("confirmed files resent on replay: %#v", retryNames)
	}
}

func TestRetryRejectsChangedFileBeforeTelegramAndRotatesDescriptor(t *testing.T) {
	root := canonicalTempDir(t)
	work := filepath.Join(root, "work")
	state := filepath.Join(root, "state")
	mustMkdir(t, work)
	mustMkdir(t, state)
	path := filepath.Join(work, "mutable.txt")
	mustWrite(t, path, "original")
	now := time.Unix(2_000_000_000, 0)
	service := openService(t, documentSenderFunc(func(context.Context, telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
		return telegram.FileReceipt{}, telegram.ErrDeliveryUnknown
	}), state, []string{work}, now)
	first, err := service.DeliverFinal(context.Background(), "provider-final-mutated", markdownLinks(path))
	if err != nil || first.Retry == nil {
		t.Fatalf("DeliverFinal() = %#v, %v", first, err)
	}
	if err := os.WriteFile(path, []byte("MUTATED!"), 0o600); err != nil {
		t.Fatalf("mutate file: %v", err)
	}
	var retrySends int
	reopened := openService(t, documentSenderFunc(func(context.Context, telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
		retrySends++
		return telegram.FileReceipt{MessageID: 90, ChatID: 4242, FileID: "changed", FileUniqueID: "changed-unique"}, nil
	}), state, []string{work}, now.Add(time.Second))
	result, err := reopened.Retry(context.Background(), first.Retry.Token)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if retrySends != 0 || result.Summary.Confirmed != 0 || result.Summary.Unconfirmed != 1 || result.Retry == nil || result.Retry.Token == first.Retry.Token {
		t.Fatalf("changed-file retry = sends %d result %#v", retrySends, result)
	}
}

func TestAmbiguousManualRetryClaimsOldDescriptorAndIssuesFreshOne(t *testing.T) {
	root := canonicalTempDir(t)
	work := filepath.Join(root, "work")
	state := filepath.Join(root, "state")
	mustMkdir(t, work)
	mustMkdir(t, state)
	path := filepath.Join(work, "ambiguous.txt")
	mustWrite(t, path, "payload")
	now := time.Unix(2_000_000_000, 0)
	var sends int
	service := openService(t, documentSenderFunc(func(context.Context, telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
		sends++
		return telegram.FileReceipt{}, telegram.ErrDeliveryUnknown
	}), state, []string{work}, now)
	first, err := service.DeliverFinal(context.Background(), "provider-final-ambiguous", markdownLinks(path))
	if err != nil || first.Retry == nil {
		t.Fatalf("DeliverFinal() = %#v, %v", first, err)
	}
	second, err := service.Retry(context.Background(), first.Retry.Token)
	if err != nil || second.Retry == nil || second.Retry.Token == first.Retry.Token {
		t.Fatalf("first Retry() = %#v, %v", second, err)
	}
	afterFirstRetry := sends
	if _, err := service.Retry(context.Background(), first.Retry.Token); !errors.Is(err, artifactproduction.ErrInvalidRetry) {
		t.Fatalf("replayed old Retry() error = %v", err)
	}
	if sends != afterFirstRetry {
		t.Fatalf("replayed token reached Telegram: %d -> %d", afterFirstRetry, sends)
	}
}

func TestWrongChatReceiptCannotConfirmArtifact(t *testing.T) {
	root := canonicalTempDir(t)
	work := filepath.Join(root, "work")
	state := filepath.Join(root, "state")
	mustMkdir(t, work)
	mustMkdir(t, state)
	path := filepath.Join(work, "wrong-chat.txt")
	mustWrite(t, path, "payload")
	service := openService(t, documentSenderFunc(func(_ context.Context, request telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
		return telegram.FileReceipt{MessageID: 91, ChatID: request.ChatID + 1, FileID: "remote", FileUniqueID: "remote-unique"}, nil
	}), state, []string{work}, time.Unix(2_000_000_000, 0))
	result, err := service.DeliverFinal(context.Background(), "provider-final-wrong-chat", markdownLinks(path))
	if err != nil {
		t.Fatalf("DeliverFinal() error = %v", err)
	}
	if result.Summary.Confirmed != 0 || result.Summary.Unconfirmed != 1 || result.Retry == nil {
		t.Fatalf("wrong-chat result = %#v", result)
	}
}

func TestRetryRejectsTamperedAndExpiredDescriptorsBeforeTelegram(t *testing.T) {
	root := canonicalTempDir(t)
	work := filepath.Join(root, "work")
	mustMkdir(t, work)
	path := filepath.Join(work, "report.txt")
	mustWrite(t, path, "report")
	state := filepath.Join(root, "state")
	mustMkdir(t, state)
	now := time.Unix(2_000_000_000, 0)
	var sends int
	sender := documentSenderFunc(func(context.Context, telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
		sends++
		return telegram.FileReceipt{}, telegram.ErrDeliveryUnknown
	})
	service := openService(t, sender, state, []string{work}, now)
	result, err := service.DeliverFinal(context.Background(), "provider-final-2", markdownLinks(path))
	if err != nil || result.Retry == nil {
		t.Fatalf("DeliverFinal() = %#v, %v", result, err)
	}
	initialSends := sends
	tampered := result.Retry.Token[:len(result.Retry.Token)-1] + "A"
	if tampered == result.Retry.Token {
		tampered = result.Retry.Token[:len(result.Retry.Token)-1] + "B"
	}
	if _, err := service.Retry(context.Background(), tampered); !errors.Is(err, artifactproduction.ErrInvalidRetry) {
		t.Fatalf("Retry(tampered) error = %v", err)
	}
	expired := openService(t, sender, state, []string{work}, result.Retry.ExpiresAt)
	if _, err := expired.Retry(context.Background(), result.Retry.Token); !errors.Is(err, artifactproduction.ErrRetryExpired) {
		t.Fatalf("Retry(expired) error = %v", err)
	}
	if sends != initialSends {
		t.Fatalf("invalid retry reached Telegram: sends %d -> %d", initialSends, sends)
	}
}

func TestDeliveryRejectsFileOutsideCanonicalAllowedRootBeforeTelegram(t *testing.T) {
	root := canonicalTempDir(t)
	work := filepath.Join(root, "work")
	outside := filepath.Join(root, "outside")
	state := filepath.Join(root, "state")
	mustMkdir(t, work)
	mustMkdir(t, outside)
	mustMkdir(t, state)
	path := filepath.Join(outside, "secret.txt")
	mustWrite(t, path, "secret")
	service := openService(t, documentSenderFunc(func(context.Context, telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
		t.Fatal("disallowed file reached Telegram")
		return telegram.FileReceipt{}, nil
	}), state, []string{work}, time.Unix(2_000_000_000, 0))
	result, err := service.DeliverFinal(context.Background(), "provider-final-3", markdownLinks(path))
	if err != nil {
		t.Fatalf("DeliverFinal() error = %v", err)
	}
	if result.Summary.Confirmed != 0 || result.Summary.Unconfirmed != 1 || result.Retry == nil {
		t.Fatalf("disallowed-file result = %#v", result)
	}
}

func TestOpenRejectsSymlinkAllowedRoot(t *testing.T) {
	root := canonicalTempDir(t)
	realWork := filepath.Join(root, "real-work")
	state := filepath.Join(root, "state")
	mustMkdir(t, realWork)
	mustMkdir(t, state)
	link := filepath.Join(root, "work-link")
	if err := os.Symlink(realWork, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := artifactproduction.Open(documentSenderFunc(nil), artifactproduction.Config{
		ManifestDirectory: filepath.Join(state, "manifests"), RetryDirectory: filepath.Join(state, "retry"),
		AllowedRoots: []string{link}, MaxFileBytes: 1024, ChatID: 4242,
		RetryKey: []byte("0123456789abcdef0123456789abcdef"), RetryTTL: time.Hour,
	})
	if !errors.Is(err, artifactproduction.ErrInvalidConfiguration) {
		t.Fatalf("Open(symlink root) error = %v", err)
	}
}

func openService(t *testing.T, sender documentSenderFunc, state string, roots []string, now time.Time) *artifactproduction.Service {
	t.Helper()
	service, err := artifactproduction.Open(sender, artifactproduction.Config{
		ManifestDirectory: filepath.Join(state, "manifests"), RetryDirectory: filepath.Join(state, "retry"),
		AllowedRoots: roots, MaxFileBytes: 1024, ChatID: 4242,
		RetryKey: []byte("0123456789abcdef0123456789abcdef"), RetryTTL: time.Hour,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return service
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return resolved
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func markdownLinks(paths ...string) string {
	result := ""
	for _, path := range paths {
		result += fmt.Sprintf("[artifact](file://%s) ", path)
	}
	return result
}
