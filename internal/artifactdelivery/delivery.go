// Package artifactdelivery turns local file links from one provider final into
// a durable, explicitly retryable Telegram delivery group. Service runs on the
// executor that owns the paths; Transport may proxy the bounded stream to the
// coordinator that owns Telegram.
package artifactdelivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"bria/internal/files"
)

var (
	ErrInvalidService = errors.New("invalid artifact delivery service")
	ErrManifestAbsent = errors.New("artifact delivery manifest does not exist")
	ErrFinalMismatch  = errors.New("provider final does not match durable artifact manifest")
	errIncompleteRead = errors.New("transport did not consume complete artifact stream")
)

var failureCodePattern = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

var deliveryOperationLocks sync.Map

// TransportFile is a verified, bounded stream. Transport must not read more
// than Size bytes and must consume Content before Send returns.
type TransportFile struct {
	FileID      string
	Attempt     uint32
	OperationID string
	FileName    string
	Size        int64
	Content     io.Reader
}

// Receipt is the durable evidence of a confirmed external delivery.
type Receipt struct {
	MessageID    int64
	FileID       string
	FileUniqueID string
}

// Transport sends one file, locally or through the coordinator, and returns a
// receipt only for unambiguous Telegram success.
type Transport interface {
	Send(context.Context, TransportFile) (Receipt, error)
}

// FailureCoder lets a transport expose a bounded, non-sensitive diagnostic
// category. Raw transport errors are never persisted by this package.
type FailureCoder interface {
	DeliveryFailureCode() string
}

// ManifestStore durably saves one group before and after external attempts.
type ManifestStore interface {
	Load(context.Context, string) (files.DeliveryManifest, bool, error)
	Save(context.Context, files.DeliveryManifest) error
}

// Service coordinates verified streaming and durable delivery state.
type Service struct {
	Opener    files.Opener
	Store     ManifestStore
	Transport Transport
}

// Summary is the one group-level result consumed by the UI.
type Summary struct {
	FinalID            string
	Total              int
	Confirmed          int
	Unconfirmed        int
	NeedsExplicitRetry bool
}

// DeliverFinal creates or resumes the group. It never automatically retries a
// file for which any previous attempt was durably recorded.
func (s Service) DeliverFinal(ctx context.Context, finalID, final string) (Summary, error) {
	if err := s.validate(); err != nil {
		return Summary{}, err
	}
	links, err := files.ExtractFinalLinks(final)
	if err != nil {
		return Summary{}, err
	}
	expected, err := files.NewDeliveryManifest(finalID, links)
	if err != nil {
		return Summary{}, err
	}
	operationLock := operationLockFor(finalID)
	operationLock.Lock()
	defer operationLock.Unlock()
	manifest, found, err := s.Store.Load(ctx, finalID)
	if err != nil {
		return Summary{}, err
	}
	if !found {
		manifest = expected
		if err := s.Store.Save(ctx, manifest); err != nil {
			return Summary{}, err
		}
	} else if !sameFiles(manifest, expected) {
		return Summary{}, ErrFinalMismatch
	}
	return s.process(ctx, manifest, false)
}

// Retry explicitly retries only files without a confirmed receipt.
func (s Service) Retry(ctx context.Context, finalID string) (Summary, error) {
	if err := s.validate(); err != nil {
		return Summary{}, err
	}
	operationLock := operationLockFor(finalID)
	operationLock.Lock()
	defer operationLock.Unlock()
	manifest, found, err := s.Store.Load(ctx, finalID)
	if err != nil {
		return Summary{}, err
	}
	if !found {
		return Summary{}, ErrManifestAbsent
	}
	return s.process(ctx, manifest, true)
}

func operationLockFor(finalID string) *sync.Mutex {
	value, _ := deliveryOperationLocks.LoadOrStore(finalID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s Service) process(ctx context.Context, manifest files.DeliveryManifest, explicit bool) (Summary, error) {
	for index := range manifest.Files {
		file := manifest.Files[index]
		if file.State == files.DeliveryConfirmed {
			continue
		}
		if !explicit && file.Attempts > 0 {
			continue
		}
		if err := manifest.MarkAttempt(file.FileID); err != nil {
			return Summary{}, err
		}
		if err := s.Store.Save(ctx, manifest); err != nil {
			return Summary{}, err
		}

		verified, err := s.Opener.OpenRegular(file.Path)
		if err != nil {
			if err := markAndSaveUnconfirmed(ctx, s.Store, &manifest, file.FileID, "file_unavailable"); err != nil {
				return Summary{}, err
			}
			continue
		}
		attempt := manifest.Files[index].Attempts
		receipt, sendErr := s.sendVerified(ctx, file.FileID, attempt, verified)
		if sendErr != nil {
			if err := markAndSaveUnconfirmed(ctx, s.Store, &manifest, file.FileID, failureCode(sendErr)); err != nil {
				return Summary{}, err
			}
			continue
		}
		receiptID, valid := durableReceiptID(receipt)
		if !valid {
			if err := markAndSaveUnconfirmed(ctx, s.Store, &manifest, file.FileID, "invalid_receipt"); err != nil {
				return Summary{}, err
			}
			continue
		}
		if err := manifest.MarkConfirmed(file.FileID, receiptID); err != nil {
			return Summary{}, err
		}
		if err := s.Store.Save(ctx, manifest); err != nil {
			return Summary{}, err
		}
	}
	return summarize(manifest), nil
}

func (s Service) sendVerified(ctx context.Context, fileID string, attempt uint32, verified *files.VerifiedFile) (Receipt, error) {
	defer verified.Close()
	content := &exactReader{reader: verified, remaining: verified.Size}
	receipt, err := s.Transport.Send(ctx, TransportFile{
		FileID:      fileID,
		Attempt:     attempt,
		OperationID: "artifact-delivery:" + fileID + ":" + strconv.FormatUint(uint64(attempt), 10),
		FileName:    filepath.Base(verified.Path),
		Size:        verified.Size,
		Content:     content,
	})
	if err == nil && (content.read != verified.Size || !content.sawEOF) {
		return Receipt{}, errIncompleteRead
	}
	return receipt, err
}

type exactReader struct {
	reader    io.Reader
	remaining int64
	read      int64
	sawEOF    bool
}

func (r *exactReader) Read(destination []byte) (int, error) {
	if r.remaining == 0 {
		r.sawEOF = true
		return 0, io.EOF
	}
	if int64(len(destination)) > r.remaining {
		destination = destination[:r.remaining]
	}
	read, err := r.reader.Read(destination)
	r.read += int64(read)
	r.remaining -= int64(read)
	if err == io.EOF {
		r.sawEOF = true
	}
	return read, err
}

func (s Service) validate() error {
	if s.Store == nil || s.Transport == nil {
		return ErrInvalidService
	}
	return nil
}

func markAndSaveUnconfirmed(ctx context.Context, store ManifestStore, manifest *files.DeliveryManifest, fileID, code string) error {
	if err := manifest.MarkUnconfirmed(fileID, code); err != nil {
		return err
	}
	return store.Save(ctx, *manifest)
}

func failureCode(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	var coded FailureCoder
	if errors.As(err, &coded) {
		if code := coded.DeliveryFailureCode(); failureCodePattern.MatchString(code) {
			return code
		}
	}
	return "transport_error"
}

func sameFiles(left, right files.DeliveryManifest) bool {
	if left.FinalID != right.FinalID || len(left.Files) != len(right.Files) {
		return false
	}
	for index := range left.Files {
		if left.Files[index].FileID != right.Files[index].FileID || left.Files[index].Path != right.Files[index].Path {
			return false
		}
	}
	return true
}

func durableReceiptID(receipt Receipt) (string, bool) {
	if receipt.MessageID <= 0 || !validTelegramFileIdentity(receipt.FileID) || !validTelegramFileIdentity(receipt.FileUniqueID) {
		return "", false
	}
	hash := sha256.Sum256([]byte(receipt.FileID + "\x00" + receipt.FileUniqueID))
	return "telegram:" + strconv.FormatInt(receipt.MessageID, 10) + ":" + hex.EncodeToString(hash[:16]), true
}

func validTelegramFileIdentity(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func summarize(manifest files.DeliveryManifest) Summary {
	summary := Summary{FinalID: manifest.FinalID, Total: len(manifest.Files)}
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
