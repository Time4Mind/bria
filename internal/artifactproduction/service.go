package artifactproduction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"bria/internal/artifactdelivery"
	"bria/internal/files"
	"bria/internal/telegram"
)

var ErrInvalidConfiguration, ErrInvalidRetry, ErrRetryExpired, ErrRetryRecoveryRequired, ErrArtifactChanged = errors.New("artifact production configuration is invalid"), errors.New("artifact retry descriptor is invalid"), errors.New("artifact retry descriptor is expired"), errors.New("artifact retry claim requires fenced recovery"), errors.New("artifact content changed after its first delivery attempt")

type DocumentSender interface {
	SendDocument(context.Context, telegram.SendDocumentRequest) (telegram.FileReceipt, error)
}

type Config struct {
	ManifestDirectory string
	RetryDirectory    string
	AllowedRoots      []string
	MaxFileBytes      int64
	ChatID            telegram.ChatID
	RetryKey          []byte
	RetryTTL          time.Duration
	Now               func() time.Time
}

type RetryDescriptor struct {
	Token     string
	ExpiresAt time.Time
}

type Result struct {
	Summary artifactdelivery.Summary
	Retry   *RetryDescriptor
}
type RecoveredRetry struct {
	FinalID        string
	Summary        artifactdelivery.Summary
	Retry          *RetryDescriptor
	recoveredClaim bool
}

type Service struct {
	delivery      artifactdelivery.Service
	manifestStore artifactdelivery.ManifestStore
	retries       *retryStore
}

func Open(sender DocumentSender, config Config) (*Service, error) {
	if nilPort(sender) || config.ChatID == 0 || config.MaxFileBytes <= 0 || config.MaxFileBytes > 50<<20 ||
		len(config.RetryKey) < 32 || config.RetryTTL <= 0 || config.RetryTTL > 30*24*time.Hour {
		return nil, ErrInvalidConfiguration
	}
	manifestDirectory, err := preparePrivateDirectory(config.ManifestDirectory)
	if err != nil {
		return nil, fmt.Errorf("prepare artifact manifest directory: %w", err)
	}
	retryDirectory, err := preparePrivateDirectory(config.RetryDirectory)
	if err != nil {
		return nil, fmt.Errorf("prepare artifact retry directory: %w", err)
	}
	if pathsOverlap(manifestDirectory, retryDirectory) {
		return nil, ErrInvalidConfiguration
	}
	roots, err := canonicalRoots(config.AllowedRoots)
	if err != nil {
		return nil, err
	}
	for _, root := range roots {
		if pathsOverlap(root, manifestDirectory) || pathsOverlap(root, retryDirectory) {
			return nil, ErrInvalidConfiguration
		}
	}
	manifestStore, err := artifactdelivery.OpenFileStore(manifestDirectory)
	if err != nil {
		return nil, fmt.Errorf("open artifact manifest store: %w", err)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	retries := &retryStore{directory: retryDirectory, key: append([]byte(nil), config.RetryKey...), ttl: config.RetryTTL, now: now}
	integrityDirectory, err := preparePrivateDirectory(filepath.Join(retryDirectory, "integrity"))
	if err != nil {
		return nil, fmt.Errorf("prepare artifact integrity directory: %w", err)
	}
	integrity := &integrityStore{directory: integrityDirectory}
	return &Service{delivery: artifactdelivery.Service{Opener: files.Opener{AllowedRoots: roots, MaxBytes: config.MaxFileBytes}, Store: manifestStore, Transport: telegramTransport{sender: sender, chatID: config.ChatID, maxBytes: config.MaxFileBytes, integrity: integrity}}, manifestStore: manifestStore, retries: retries}, nil
}

func (service *Service) DeliverFinal(ctx context.Context, finalID, final string) (Result, error) {
	if service == nil || service.retries == nil || ctx == nil {
		return Result{}, ErrInvalidConfiguration
	}
	summary, err := service.delivery.DeliverFinal(ctx, finalID, final)
	if err != nil {
		return Result{}, err
	}
	return service.result(ctx, summary)
}

func (service *Service) Retry(ctx context.Context, token string) (Result, error) {
	if service == nil || service.retries == nil || ctx == nil {
		return Result{}, ErrInvalidConfiguration
	}
	claim, err := service.retries.resolve(ctx, token, func(finalID string) (string, error) {
		return service.manifestFence(ctx, finalID)
	})
	if err != nil {
		return Result{}, err
	}
	summary, err := service.delivery.Retry(ctx, claim.FinalID)
	if err != nil {
		return Result{}, err
	}
	result := Result{Summary: summary}
	if !summary.NeedsExplicitRetry {
		if resolved, resolveErr := service.retries.resolveClaimed(ctx, claim); resolveErr != nil || !resolved {
			if resolveErr != nil {
				return Result{}, resolveErr
			}
			return Result{}, ErrRetryRecoveryRequired
		}
		return result, nil
	}
	descriptor, rotated, err := service.retries.rotateClaimed(ctx, claim)
	if err != nil {
		return Result{}, err
	}
	if !rotated {
		return Result{}, ErrRetryRecoveryRequired
	}
	result.Retry = &descriptor
	return result, nil
}

func (service *Service) RecoverClaimedResults(ctx context.Context) ([]RecoveredRetry, error) {
	if service == nil || service.retries == nil || service.manifestStore == nil || ctx == nil {
		return nil, ErrInvalidConfiguration
	}
	records, err := service.retries.recoverable(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]RecoveredRetry, 0, len(records))
	for _, record := range records {
		manifest, found, err := service.manifestStore.Load(ctx, record.FinalID)
		if err != nil || !found {
			if err == nil {
				err = artifactdelivery.ErrManifestAbsent
			}
			return nil, err
		}
		summary := summarizeManifest(manifest)
		entry := RecoveredRetry{FinalID: record.FinalID, Summary: summary, recoveredClaim: record.State == retryClaimed}
		if !summary.NeedsExplicitRetry {
			if record.State == retryClaimed {
				resolved, err := service.retries.resolveClaimed(ctx, record)
				if err != nil || !resolved {
					return nil, ErrRetryRecoveryRequired
				}
			}
			result = append(result, entry)
			continue
		}
		descriptor, rotated, err := service.retries.rotateClaimed(ctx, record)
		if record.State == retryIssued {
			descriptor, err = service.retries.ensure(ctx, record.FinalID)
			rotated = err == nil
		}
		if err != nil {
			return nil, err
		}
		if rotated {
			entry.Retry = &descriptor
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].FinalID < result[right].FinalID })
	return result, nil
}

func (service *Service) RecoverClaimed(ctx context.Context) ([]RetryDescriptor, error) {
	recovered, err := service.RecoverClaimedResults(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]RetryDescriptor, 0, len(recovered))
	for _, entry := range recovered {
		if entry.Retry != nil && entry.recoveredClaim {
			result = append(result, *entry.Retry)
		}
	}
	return result, nil
}

func summarizeManifest(manifest files.DeliveryManifest) artifactdelivery.Summary {
	summary := artifactdelivery.Summary{FinalID: manifest.FinalID, Total: len(manifest.Files)}
	for _, file := range manifest.Files {
		if file.State == files.DeliveryConfirmed {
			summary.Confirmed++
		} else {
			summary.Unconfirmed++
			summary.NeedsExplicitRetry = true
		}
	}
	return summary
}

func (service *Service) manifestFence(ctx context.Context, finalID string) (string, error) {
	manifest, found, err := service.manifestStore.Load(ctx, finalID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", artifactdelivery.ErrManifestAbsent
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(manifest.FinalID))
	for _, file := range manifest.Files {
		for _, field := range []string{
			file.FileID, file.Path, string(file.State), strconv.FormatUint(uint64(file.Attempts), 10), file.ReceiptID, file.FailureCode,
		} {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(field))
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (service *Service) result(ctx context.Context, summary artifactdelivery.Summary) (Result, error) {
	result := Result{Summary: summary}
	if !summary.NeedsExplicitRetry {
		return result, nil
	}
	descriptor, err := service.retries.ensure(ctx, summary.FinalID)
	if err != nil {
		return Result{}, err
	}
	result.Retry = &descriptor
	return result, nil
}

type telegramTransport struct {
	sender    DocumentSender
	chatID    telegram.ChatID
	maxBytes  int64
	integrity *integrityStore
}

func (transport telegramTransport) Send(ctx context.Context, file artifactdelivery.TransportFile) (artifactdelivery.Receipt, error) {
	if ctx == nil || nilPort(transport.sender) || transport.chatID == 0 || transport.maxBytes <= 0 || transport.integrity == nil ||
		file.Size < 0 || file.Size > transport.maxBytes || file.Content == nil || filepath.Base(file.FileName) != file.FileName {
		return artifactdelivery.Receipt{}, ErrInvalidConfiguration
	}
	content, err := io.ReadAll(io.LimitReader(file.Content, transport.maxBytes+1))
	if err != nil || int64(len(content)) != file.Size || int64(len(content)) > transport.maxBytes {
		return artifactdelivery.Receipt{}, errors.New("read verified artifact content")
	}
	priorReceipt, err := transport.integrity.begin(ctx, file, transport.chatID, content)
	if err != nil {
		return artifactdelivery.Receipt{}, err
	}
	if priorReceipt != nil {
		return artifactdelivery.Receipt{MessageID: int64(priorReceipt.MessageID), FileID: priorReceipt.FileID, FileUniqueID: priorReceipt.FileUniqueID}, nil
	}
	contentType := mime.TypeByExtension(filepath.Ext(file.FileName))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	receipt, err := transport.sender.SendDocument(ctx, telegram.SendDocumentRequest{ChatID: transport.chatID, FileName: file.FileName, ContentType: contentType, Content: content})
	if err != nil {
		_ = transport.integrity.unknown(context.WithoutCancel(ctx), file)
		return artifactdelivery.Receipt{}, err
	}
	if receipt.ChatID != transport.chatID || receipt.MessageID <= 0 || strings.TrimSpace(receipt.FileID) == "" || strings.TrimSpace(receipt.FileUniqueID) == "" {
		_ = transport.integrity.unknown(context.WithoutCancel(ctx), file)
		return artifactdelivery.Receipt{}, errors.New("Telegram artifact receipt does not match the requested delivery")
	}
	if err := transport.integrity.confirm(context.WithoutCancel(ctx), file, receipt); err != nil {
		return artifactdelivery.Receipt{}, err
	}
	return artifactdelivery.Receipt{MessageID: int64(receipt.MessageID), FileID: receipt.FileID, FileUniqueID: receipt.FileUniqueID}, nil
}

func canonicalRoots(configured []string) ([]string, error) {
	if len(configured) == 0 || len(configured) > 64 {
		return nil, ErrInvalidConfiguration
	}
	result := make([]string, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for _, root := range configured {
		canonical, err := validateExistingDirectory(root, false)
		if err != nil || canonical == filepath.VolumeName(canonical)+string(filepath.Separator) {
			return nil, ErrInvalidConfiguration
		}
		if _, exists := seen[canonical]; exists {
			return nil, ErrInvalidConfiguration
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result, nil
}

func pathsOverlap(left, right string) bool {
	leftToRight, leftErr := filepath.Rel(left, right)
	rightToLeft, rightErr := filepath.Rel(right, left)
	return (leftErr == nil && !escapes(leftToRight)) || (rightErr == nil && !escapes(rightToLeft))
}

func escapes(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative)
}

func nilPort(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ artifactdelivery.Transport = telegramTransport{}
