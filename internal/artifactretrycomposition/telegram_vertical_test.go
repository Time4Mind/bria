package artifactretrycomposition_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/artifactcomposition"
	"bria/internal/artifactdelivery"
	"bria/internal/artifactproduction"
	"bria/internal/artifactretrycomposition"
	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/turnprocessing"
)

type documentSequence struct {
	requests []telegram.SendDocumentRequest
	unknown  map[string]bool
}

func (sender *documentSequence) SendDocument(_ context.Context, request telegram.SendDocumentRequest) (telegram.FileReceipt, error) {
	sender.requests = append(sender.requests, request)
	if sender.unknown[request.FileName] {
		return telegram.FileReceipt{}, telegram.ErrDeliveryUnknown
	}
	return telegram.FileReceipt{ChatID: request.ChatID, MessageID: telegram.MessageID(800 + len(sender.requests)), FileID: "file", FileUniqueID: "unique-" + request.FileName}, nil
}

type telegramSequence struct {
	sends, edits int
	status       coordinator.Status
	keyboard     *coordinator.KeyboardMarkup
}

func (sender *telegramSequence) SendStatus(_ context.Context, _ string, status coordinator.Status) (coordinator.Receipt, error) {
	sender.sends++
	sender.status = status
	return coordinator.Receipt{MessageID: 700}, nil
}

func (sender *telegramSequence) SendStatusWithKeyboard(_ context.Context, _ string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	sender.sends++
	sender.status, sender.keyboard = status, keyboard
	return coordinator.Receipt{MessageID: 700}, nil
}

func (sender *telegramSequence) EditStatusWithKeyboard(_ context.Context, _ string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	sender.edits++
	sender.status, sender.keyboard = status, keyboard
	return coordinator.Receipt{MessageID: status.SourceMessageID}, nil
}

type fallbackCallbacks struct{ calls int }

func (fallback *fallbackCallbacks) HandleCallback(context.Context, telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	fallback.calls++
	return telegramflow.CallbackResult{}, nil
}

type verticalMessages struct{}

func (verticalMessages) HandleMessage(context.Context, coordinator.Update) (telegramflow.MessageResult, error) {
	return telegramflow.MessageResult{}, nil
}

type crashAfterPublish struct {
	next *artifactretrycomposition.TelegramPublisher
}

func (publisher crashAfterPublish) PublishArtifactRetry(ctx context.Context, notice artifactretrycomposition.Notice) error {
	if err := publisher.next.PublishArtifactRetry(ctx, notice); err != nil {
		return err
	}
	return errors.New("crash after durable summary enqueue")
}

func TestTwoFilesRestartAndOneShotManualRetryVertical(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"confirmed.txt": "confirmed", "unknown.txt": "unknown"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	final := turnprocessing.FinalObservation{
		SessionID: testSessionID, MessageID: "telegram-update:51", OperationID: "telegram-update:51:final",
		Text: "[confirmed](file://" + filepath.Join(work, "confirmed.txt") + ") [unknown](file://" + filepath.Join(work, "unknown.txt") + ")",
	}
	documents := &documentSequence{unknown: map[string]bool{"unknown.txt": true}}
	artifacts := openArtifacts(t, root, work, documents, now)
	retries, err := artifactretrycomposition.Open(filepath.Join(root, "artifact-retry.json"), artifacts, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter := verticalPresenter(t, now)
	registryPath, operationsPath := filepath.Join(root, "callbacks.json"), filepath.Join(root, "operations.json")
	registry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	operations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	transport := &telegramSequence{}
	fallback := &fallbackCallbacks{}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: telegramstate.NewMemoryStore(), MessageUI: verticalMessages{},
		Callbacks: artifactretrycomposition.WrapCallback(retries, fallback), Operations: operations, Sender: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := artifactretrycomposition.NewTelegramPublisher(42, presenter, outbound)
	if err != nil {
		t.Fatal(err)
	}
	if err := retries.BindPublisher(publisher); err != nil {
		t.Fatal(err)
	}
	if err := retries.ProcessFinal(context.Background(), final); err != nil {
		t.Fatalf("ProcessFinal() error = %v", err)
	}
	if len(documents.requests) != 2 || transport.sends != 1 || transport.keyboard == nil || len(*transport.keyboard) != 1 || len((*transport.keyboard)[0]) != 1 {
		t.Fatalf("initial requests/summary/button = %d/%d/%#v", len(documents.requests), transport.sends, transport.keyboard)
	}
	if transport.status.Text != "Не подтверждена доставка 1 из 2 файлов. Повторить можно только вручную; возможен дубль." {
		t.Fatalf("summary text = %q", transport.status.Text)
	}
	token := (*transport.keyboard)[0][0].CallbackData

	// Reopening every durable component performs no artifact or Telegram send.
	now = now.Add(time.Minute)
	retryDocuments := &documentSequence{unknown: map[string]bool{"unknown.txt": true}}
	reopenedArtifacts := openArtifacts(t, root, work, retryDocuments, now)
	reopenedRetries, err := artifactretrycomposition.Open(filepath.Join(root, "artifact-retry.json"), reopenedArtifacts, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reopenedRegistry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reopenedOperations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	reopenedTransport := &telegramSequence{}
	reopenedHandler, reopenedOutbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: reopenedRegistry,
		UIState: telegramstate.NewMemoryStore(), MessageUI: verticalMessages{},
		Callbacks: artifactretrycomposition.WrapCallback(reopenedRetries, fallback), Operations: reopenedOperations, Sender: reopenedTransport,
	})
	if err != nil {
		t.Fatal(err)
	}
	reopenedPublisher, err := artifactretrycomposition.NewTelegramPublisher(42, presenter, reopenedOutbound)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopenedRetries.BindPublisher(reopenedPublisher); err != nil {
		t.Fatal(err)
	}
	if err := reopenedRetries.ProcessFinal(context.Background(), final); err != nil {
		t.Fatal(err)
	}
	if len(retryDocuments.requests) != 0 || reopenedTransport.sends != 0 {
		t.Fatalf("restart auto sends = documents %d summaries %d", len(retryDocuments.requests), reopenedTransport.sends)
	}

	click := coordinator.Update{ID: 900, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: token, CallbackQueryID: "retry-900", SourceMessageID: 700}
	decision, err := reopenedHandler.Handle(context.Background(), click)
	if err != nil {
		t.Fatalf("manual retry callback error = %v", err)
	}
	if len(retryDocuments.requests) != 1 || retryDocuments.requests[0].FileName != "unknown.txt" || fallback.calls != 0 || decision.Keyboard == nil || len(*decision.Keyboard) != 1 || len((*decision.Keyboard)[0]) != 1 {
		t.Fatalf("retry requests/fallback/decision = %#v/%d/%#v", retryDocuments.requests, fallback.calls, decision)
	}
	if _, err := reopenedOutbound.EditStatusWithKeyboard(context.Background(), "status:900", decision.Status, decision.Keyboard); err != nil {
		t.Fatal(err)
	}
	nextToken := (*decision.Keyboard)[0][0].CallbackData

	second := click
	second.ID, second.CallbackQueryID = 901, "retry-901"
	if _, err := reopenedHandler.Handle(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if len(retryDocuments.requests) != 1 {
		t.Fatalf("second click resent files: %d", len(retryDocuments.requests))
	}
	tampered := click
	tampered.ID, tampered.CallbackQueryID = 902, "retry-902"
	tampered.Text = token[:len(token)-1] + string([]byte{token[len(token)-1] ^ 1})
	if _, err := reopenedHandler.Handle(context.Background(), tampered); err != nil {
		t.Fatal(err)
	}
	if len(retryDocuments.requests) != 1 {
		t.Fatalf("tampered callback resent files: %d", len(retryDocuments.requests))
	}

	// The Unknown retry response survives another restart as a fresh manual
	// decision and still sends only the one unconfirmed file when clicked.
	now = now.Add(time.Minute)
	finalDocuments := &documentSequence{unknown: map[string]bool{}}
	finalArtifacts := openArtifacts(t, root, work, finalDocuments, now)
	finalRetries, err := artifactretrycomposition.Open(filepath.Join(root, "artifact-retry.json"), finalArtifacts, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	finalRegistry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	finalOperations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	finalTransport := &telegramSequence{}
	finalHandler, finalOutbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: finalRegistry,
		UIState: telegramstate.NewMemoryStore(), MessageUI: verticalMessages{},
		Callbacks: artifactretrycomposition.WrapCallback(finalRetries, fallback), Operations: finalOperations, Sender: finalTransport,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalPublisher, _ := artifactretrycomposition.NewTelegramPublisher(42, presenter, finalOutbound)
	_ = finalRetries.BindPublisher(finalPublisher)
	if len(finalDocuments.requests) != 0 || finalTransport.sends != 0 {
		t.Fatal("restart automatically replayed the Unknown retry")
	}
	finalClick := coordinator.Update{ID: 903, Kind: coordinator.UpdateCallback, ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: nextToken, CallbackQueryID: "retry-903", SourceMessageID: 700}
	finalDecision, err := finalHandler.Handle(context.Background(), finalClick)
	if err != nil || finalDecision.Keyboard == nil || len(*finalDecision.Keyboard) != 0 {
		t.Fatalf("final manual decision = %#v, %v", finalDecision, err)
	}
	if len(finalDocuments.requests) != 1 || finalDocuments.requests[0].FileName != "unknown.txt" {
		t.Fatalf("final retry requests = %#v", finalDocuments.requests)
	}
}

func TestCrashAfterSummaryEnqueueBeforeMarkReplaysStableOperationOnce(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	root := t.TempDir()
	presenter := verticalPresenter(t, now)
	registryPath, operationsPath := filepath.Join(root, "callbacks.json"), filepath.Join(root, "operations.json")
	registry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	operations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	transport := &telegramSequence{}
	_, outbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: registry,
		UIState: telegramstate.NewMemoryStore(), MessageUI: verticalMessages{},
		Callbacks: &fallbackCallbacks{}, Operations: operations, Sender: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := artifactretrycomposition.NewTelegramPublisher(42, presenter, outbound)
	if err != nil {
		t.Fatal(err)
	}
	delivery := &deliveryStub{initial: artifactproduction.Result{
		Summary: artifactdelivery.Summary{Total: 2, Confirmed: 1, Unconfirmed: 1, NeedsExplicitRetry: true},
		Retry:   &artifactproduction.RetryDescriptor{Token: "descriptor", ExpiresAt: now.Add(time.Hour)},
	}}
	retryPath := filepath.Join(root, "retry.json")
	retries, err := artifactretrycomposition.Open(retryPath, delivery, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := retries.BindPublisher(crashAfterPublish{next: publisher}); err != nil {
		t.Fatal(err)
	}
	final := turnprocessing.FinalObservation{SessionID: testSessionID, MessageID: "telegram-update:61", OperationID: "telegram-update:61:final", Text: "final"}
	if err := retries.ProcessFinal(context.Background(), final); err == nil {
		t.Fatal("ProcessFinal() unexpectedly persisted the post-enqueue mark")
	}

	reopenedDelivery := &deliveryStub{}
	reopenedRetries, err := artifactretrycomposition.Open(retryPath, reopenedDelivery, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reopenedRegistry, err := telegrampipeline.OpenFileCallbackRegistry(registryPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reopenedOperations, err := telegramflow.OpenFileCallbackOperationStore(operationsPath)
	if err != nil {
		t.Fatal(err)
	}
	_, reopenedOutbound, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: 7, OwnerPrivateChatID: 42, Presenter: presenter, CallbackRegistry: reopenedRegistry,
		UIState: telegramstate.NewMemoryStore(), MessageUI: verticalMessages{},
		Callbacks: &fallbackCallbacks{}, Operations: reopenedOperations, Sender: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	reopenedPublisher, err := artifactretrycomposition.NewTelegramPublisher(42, presenter, reopenedOutbound)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopenedRetries.BindPublisher(reopenedPublisher); err != nil {
		t.Fatalf("replay after reopen error = %v", err)
	}
	if err := reopenedRetries.ProcessFinal(context.Background(), final); err != nil {
		t.Fatal(err)
	}
	if transport.sends != 1 || reopenedDelivery.deliveries != 0 || reopenedDelivery.retries != 0 {
		t.Fatalf("summary/doc sends after replay = %d/%d/%d, want 1/0/0", transport.sends, reopenedDelivery.deliveries, reopenedDelivery.retries)
	}
}

func openArtifacts(t *testing.T, root, work string, sender *documentSequence, now time.Time) *artifactcomposition.Composition {
	t.Helper()
	service, err := artifactproduction.Open(sender, artifactproduction.Config{
		ManifestDirectory: filepath.Join(root, "manifests"), RetryDirectory: filepath.Join(root, "descriptors"), AllowedRoots: []string{work},
		MaxFileBytes: 1024, ChatID: 42, RetryKey: bytes.Repeat([]byte{0x42}, 32), RetryTTL: time.Hour, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	composition, err := artifactcomposition.Open(service)
	if err != nil {
		t.Fatal(err)
	}
	return composition
}

func verticalPresenter(t *testing.T, now time.Time) *telegrambridge.Presenter {
	t.Helper()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x24}, 32), bytes.NewReader(bytes.Repeat([]byte{0x12}, 4096)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return presenter
}
