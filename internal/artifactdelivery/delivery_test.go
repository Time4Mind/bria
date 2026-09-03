package artifactdelivery_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/artifactdelivery"
	"bria/internal/files"
)

func TestDeliverFinalPersistsAttemptBeforeStreamingAndReceiptAfter(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	work := filepath.Join(directory, "work")
	mustMkdir(t, work)
	path := filepath.Join(work, "report.txt")
	mustWrite(t, path, "verified report")

	store := mustStore(t, filepath.Join(directory, "manifests"))
	transport := &fakeTransport{send: func(request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		manifest, found, err := store.Load(context.Background(), "final-1")
		if err != nil || !found {
			t.Fatalf("manifest visible before transport = found %v, err %v", found, err)
		}
		if got := manifest.Files[0].Attempts; got != 1 {
			t.Fatalf("durable attempts before transport = %d, want 1", got)
		}
		content, err := io.ReadAll(request.Content)
		if err != nil {
			t.Fatalf("read bounded content: %v", err)
		}
		if request.FileName != "report.txt" || request.Size != int64(len(content)) || string(content) != "verified report" {
			t.Fatalf("transport request = name %q size %d content %q", request.FileName, request.Size, content)
		}
		return artifactdelivery.Receipt{MessageID: 801, FileID: "remote-file", FileUniqueID: "remote-unique"}, nil
	}}
	service := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     store,
		Transport: transport,
	}

	summary, err := service.DeliverFinal(context.Background(), "final-1", markdownLink(path))
	if err != nil {
		t.Fatalf("DeliverFinal: %v", err)
	}
	wantSummary := artifactdelivery.Summary{FinalID: "final-1", Total: 1, Confirmed: 1}
	if summary != wantSummary {
		t.Fatalf("summary = %#v, want %#v", summary, wantSummary)
	}
	manifest, found, err := store.Load(context.Background(), "final-1")
	if err != nil || !found {
		t.Fatalf("reload manifest = found %v, err %v", found, err)
	}
	if got := manifest.Files[0]; got.State != files.DeliveryConfirmed || got.Attempts != 1 || !strings.HasPrefix(got.ReceiptID, "telegram:801:") {
		t.Fatalf("persisted delivery = %#v", got)
	}
}

func TestUnconfirmedDeliveryIsNotAutomaticallyRetriedAfterReopen(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	work := filepath.Join(directory, "work")
	mustMkdir(t, work)
	path := filepath.Join(work, "answer.txt")
	mustWrite(t, path, "answer")
	storePath := filepath.Join(directory, "manifests")

	var firstOperationID, firstFileID string
	firstTransport := &fakeTransport{send: func(request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		firstOperationID = request.OperationID
		firstFileID = request.FileID
		if request.Attempt != 1 || request.OperationID == "" {
			t.Fatalf("first transport identity = attempt %d operation %q", request.Attempt, request.OperationID)
		}
		return artifactdelivery.Receipt{}, errors.New("connection reset after upload")
	}}
	first := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     mustStore(t, storePath),
		Transport: firstTransport,
	}
	summary, err := first.DeliverFinal(context.Background(), "final-unknown", markdownLink(path))
	if err != nil {
		t.Fatalf("first DeliverFinal: %v", err)
	}
	if firstTransport.calls != 1 || !summary.NeedsExplicitRetry || summary.Unconfirmed != 1 {
		t.Fatalf("first result = calls %d summary %#v", firstTransport.calls, summary)
	}

	reopenedTransport := &fakeTransport{send: func(request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		if request.Attempt != 2 || request.OperationID == "" || request.OperationID == firstOperationID || request.FileID != firstFileID {
			t.Fatalf("retry identity = file %q attempt %d operation %q; first file %q operation %q", request.FileID, request.Attempt, request.OperationID, firstFileID, firstOperationID)
		}
		return consumeAndConfirm(request, "tg-message:900")
	}}
	reopened := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     mustStore(t, storePath),
		Transport: reopenedTransport,
	}
	summary, err = reopened.DeliverFinal(context.Background(), "final-unknown", markdownLink(path))
	if err != nil {
		t.Fatalf("automatic resume: %v", err)
	}
	if reopenedTransport.calls != 0 || !summary.NeedsExplicitRetry || summary.Unconfirmed != 1 {
		t.Fatalf("automatic resume retried: calls %d summary %#v", reopenedTransport.calls, summary)
	}

	summary, err = reopened.Retry(context.Background(), "final-unknown")
	if err != nil {
		t.Fatalf("explicit Retry: %v", err)
	}
	if reopenedTransport.calls != 1 || summary.Confirmed != 1 || summary.NeedsExplicitRetry {
		t.Fatalf("explicit retry result = calls %d summary %#v", reopenedTransport.calls, summary)
	}
}

func TestExplicitRetrySendsOnlyFilesWithoutConfirmedReceipt(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	work := filepath.Join(directory, "work")
	mustMkdir(t, work)
	firstPath := filepath.Join(work, "first.txt")
	secondPath := filepath.Join(work, "second.txt")
	mustWrite(t, firstPath, "first")
	mustWrite(t, secondPath, "second")

	transport := &fakeTransport{send: func(request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		if request.FileName == "second.txt" {
			return artifactdelivery.Receipt{}, errors.New("ambiguous")
		}
		return consumeAndConfirm(request, "confirmed-first")
	}}
	service := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     mustStore(t, filepath.Join(directory, "manifests")),
		Transport: transport,
	}
	final := markdownLink(firstPath) + " " + markdownLink(secondPath)
	summary, err := service.DeliverFinal(context.Background(), "final-group", final)
	if err != nil {
		t.Fatalf("DeliverFinal: %v", err)
	}
	if summary.Confirmed != 1 || summary.Unconfirmed != 1 || !summary.NeedsExplicitRetry {
		t.Fatalf("group summary = %#v", summary)
	}

	transport.send = func(request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		return consumeAndConfirm(request, "confirmed-"+request.FileName)
	}
	transport.names = nil
	summary, err = service.Retry(context.Background(), "final-group")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if !reflect.DeepEqual(transport.names, []string{"second.txt"}) {
		t.Fatalf("retried files = %#v, want only second.txt", transport.names)
	}
	if summary.Confirmed != 2 || summary.Unconfirmed != 0 || summary.NeedsExplicitRetry {
		t.Fatalf("retry summary = %#v", summary)
	}
}

func TestTransportCannotReadBytesAppendedAfterVerification(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	work := filepath.Join(directory, "work")
	mustMkdir(t, work)
	path := filepath.Join(work, "bounded.txt")
	mustWrite(t, path, "before")

	transport := &fakeTransport{send: func(request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if _, err := file.WriteString("-secret-after-open"); err != nil {
			t.Fatalf("append content: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close append: %v", err)
		}
		content, err := io.ReadAll(request.Content)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(content) != "before" || request.Size != 6 {
			t.Fatalf("bounded transport got size %d content %q", request.Size, content)
		}
		return artifactdelivery.Receipt{MessageID: 802, FileID: "bounded-file", FileUniqueID: "bounded-unique"}, nil
	}}
	service := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     mustStore(t, filepath.Join(directory, "manifests")),
		Transport: transport,
	}
	if _, err := service.DeliverFinal(context.Background(), "final-bounded", markdownLink(path)); err != nil {
		t.Fatalf("DeliverFinal: %v", err)
	}
}

func TestReceiptIsRejectedWhenTransportDidNotConsumeWholeVerifiedStream(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	work := filepath.Join(directory, "work")
	mustMkdir(t, work)
	path := filepath.Join(work, "partial.txt")
	mustWrite(t, path, "complete payload")

	transport := &fakeTransport{send: func(artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		return artifactdelivery.Receipt{MessageID: 803, FileID: "partial-file", FileUniqueID: "partial-unique"}, nil
	}}
	store := mustStore(t, filepath.Join(directory, "manifests"))
	service := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     store,
		Transport: transport,
	}
	summary, err := service.DeliverFinal(context.Background(), "final-partial", markdownLink(path))
	if err != nil {
		t.Fatalf("DeliverFinal: %v", err)
	}
	if summary.Confirmed != 0 || summary.Unconfirmed != 1 || !summary.NeedsExplicitRetry {
		t.Fatalf("partial-stream summary = %#v", summary)
	}
	manifest, _, err := store.Load(context.Background(), "final-partial")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := manifest.Files[0]; got.ReceiptID != "" || got.State != files.DeliveryUnconfirmed {
		t.Fatalf("partial-stream durable state = %#v", got)
	}
}

func TestReceiptIsRejectedWhenTransportReadsSizeButDoesNotObserveEOF(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	work := filepath.Join(directory, "work")
	mustMkdir(t, work)
	path := filepath.Join(work, "no-eof.txt")
	mustWrite(t, path, "complete payload")

	transport := &fakeTransport{send: func(request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		content := make([]byte, request.Size)
		if _, err := io.ReadFull(request.Content, content); err != nil {
			return artifactdelivery.Receipt{}, err
		}
		return artifactdelivery.Receipt{MessageID: 805, FileID: "no-eof-file", FileUniqueID: "no-eof-unique"}, nil
	}}
	store := mustStore(t, filepath.Join(directory, "manifests"))
	service := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     store,
		Transport: transport,
	}

	summary, err := service.DeliverFinal(context.Background(), "final-no-eof", markdownLink(path))
	if err != nil {
		t.Fatalf("DeliverFinal: %v", err)
	}
	if summary.Confirmed != 0 || summary.Unconfirmed != 1 || !summary.NeedsExplicitRetry {
		t.Fatalf("no-EOF summary = %#v", summary)
	}
	manifest, found, err := store.Load(context.Background(), "final-no-eof")
	if err != nil || !found {
		t.Fatalf("Load = found %v, err %v", found, err)
	}
	if got := manifest.Files[0]; got.ReceiptID != "" || got.State != files.DeliveryUnconfirmed || got.FailureCode != "transport_error" {
		t.Fatalf("no-EOF durable state = %#v", got)
	}
}

func TestWhitespaceReceiptCannotSuppressExplicitRetry(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	work := filepath.Join(directory, "work")
	mustMkdir(t, work)
	path := filepath.Join(work, "receipt.txt")
	mustWrite(t, path, "receipt")

	transport := &fakeTransport{send: func(request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		if _, err := io.Copy(io.Discard, request.Content); err != nil {
			return artifactdelivery.Receipt{}, err
		}
		return artifactdelivery.Receipt{MessageID: 804, FileID: " \t\r\n", FileUniqueID: "valid-unique"}, nil
	}}
	store := mustStore(t, filepath.Join(directory, "manifests"))
	service := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     store,
		Transport: transport,
	}
	summary, err := service.DeliverFinal(context.Background(), "final-invalid-receipt", markdownLink(path))
	if err != nil {
		t.Fatalf("DeliverFinal: %v", err)
	}
	if summary.Confirmed != 0 || summary.Unconfirmed != 1 || !summary.NeedsExplicitRetry {
		t.Fatalf("invalid receipt summary = %#v", summary)
	}
}

func TestConcurrentExplicitRetriesDoNotDuplicateConfirmedFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	work := filepath.Join(directory, "work")
	mustMkdir(t, work)
	path := filepath.Join(work, "once.txt")
	mustWrite(t, path, "once")
	store := mustStore(t, filepath.Join(directory, "manifests"))

	first := &fakeTransport{send: func(artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		return artifactdelivery.Receipt{}, errors.New("unknown")
	}}
	service := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     store,
		Transport: first,
	}
	if _, err := service.DeliverFinal(context.Background(), "final-concurrent", markdownLink(path)); err != nil {
		t.Fatalf("seed unconfirmed delivery: %v", err)
	}

	var sends atomic.Int32
	retryTransport := &concurrentTransport{send: func(request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
		sends.Add(1)
		time.Sleep(30 * time.Millisecond)
		return consumeAndConfirm(request, "one-confirmed-receipt")
	}}
	retryService := artifactdelivery.Service{
		Opener:    files.Opener{AllowedRoots: []string{work}, MaxBytes: 1024},
		Store:     store,
		Transport: retryTransport,
	}
	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := retryService.Retry(context.Background(), "final-concurrent")
			errorsFound <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("Retry: %v", err)
		}
	}
	if got := sends.Load(); got != 1 {
		t.Fatalf("concurrent retry transport sends = %d, want 1", got)
	}
}

type fakeTransport struct {
	calls int
	names []string
	send  func(artifactdelivery.TransportFile) (artifactdelivery.Receipt, error)
}

type concurrentTransport struct {
	send func(artifactdelivery.TransportFile) (artifactdelivery.Receipt, error)
}

func (f *concurrentTransport) Send(_ context.Context, request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
	return f.send(request)
}

func (f *fakeTransport) Send(_ context.Context, request artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
	f.calls++
	f.names = append(f.names, request.FileName)
	return f.send(request)
}

func mustStore(t *testing.T, directory string) *artifactdelivery.FileStore {
	t.Helper()
	store, err := artifactdelivery.OpenFileStore(directory)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	return store
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func markdownLink(path string) string {
	return fmt.Sprintf("[artifact](file://%s)", path)
}

func consumeAndConfirm(request artifactdelivery.TransportFile, receiptID string) (artifactdelivery.Receipt, error) {
	if _, err := io.Copy(io.Discard, request.Content); err != nil {
		return artifactdelivery.Receipt{}, err
	}
	return artifactdelivery.Receipt{
		MessageID:    900,
		FileID:       "file-" + receiptID,
		FileUniqueID: "unique-" + receiptID,
	}, nil
}
