package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrInvalidManifest = errors.New("invalid file delivery manifest")
	ErrUnknownFile     = errors.New("unknown delivery file")
)

var failureCodePattern = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

// DeliveryState is durable per-file delivery state.
type DeliveryState string

const (
	DeliveryPending     DeliveryState = "pending"
	DeliveryUnconfirmed DeliveryState = "unconfirmed"
	DeliveryConfirmed   DeliveryState = "confirmed"
)

// DeliveryFile contains only transport-neutral durable values.
type DeliveryFile struct {
	FileID      string        `json:"file_id"`
	Path        string        `json:"path"`
	State       DeliveryState `json:"state"`
	Attempts    uint32        `json:"attempts"`
	ReceiptID   string        `json:"receipt_id,omitempty"`
	FailureCode string        `json:"failure_code,omitempty"`
}

// DeliveryManifest tracks every file referenced by one final answer.
type DeliveryManifest struct {
	FinalID string         `json:"final_id"`
	Files   []DeliveryFile `json:"files"`
}

// NewDeliveryManifest creates stable file identifiers from finalID and path.
func NewDeliveryManifest(finalID string, links []Link) (DeliveryManifest, error) {
	if finalID == "" || strings.ContainsRune(finalID, 0) {
		return DeliveryManifest{}, ErrInvalidManifest
	}
	manifest := DeliveryManifest{FinalID: finalID, Files: make([]DeliveryFile, 0, len(links))}
	seen := make(map[string]struct{}, len(links))
	for _, link := range links {
		path := filepath.Clean(link.Path)
		if !filepath.IsAbs(path) {
			return DeliveryManifest{}, ErrInvalidManifest
		}
		if _, exists := seen[path]; exists {
			return DeliveryManifest{}, ErrInvalidManifest
		}
		seen[path] = struct{}{}
		manifest.Files = append(manifest.Files, DeliveryFile{FileID: stableFileID(finalID, path), Path: path, State: DeliveryPending})
	}
	return manifest, nil
}

// MarkAttempt records a delivery attempt without assuming its outcome.
func (m *DeliveryManifest) MarkAttempt(fileID string) error {
	file, err := m.find(fileID)
	if err != nil {
		return err
	}
	if file.State == DeliveryConfirmed || file.Attempts == ^uint32(0) {
		return ErrInvalidManifest
	}
	file.Attempts++
	file.State = DeliveryPending
	file.ReceiptID = ""
	file.FailureCode = ""
	return nil
}

// MarkUnconfirmed records an ambiguous or failed delivery outcome.
func (m *DeliveryManifest) MarkUnconfirmed(fileID, failureCode string) error {
	file, err := m.find(fileID)
	if err != nil {
		return err
	}
	if file.State == DeliveryConfirmed || file.Attempts == 0 || !failureCodePattern.MatchString(failureCode) {
		return ErrInvalidManifest
	}
	file.State = DeliveryUnconfirmed
	file.ReceiptID = ""
	file.FailureCode = failureCode
	return nil
}

// MarkConfirmed records the external transport receipt.
func (m *DeliveryManifest) MarkConfirmed(fileID, receiptID string) error {
	file, err := m.find(fileID)
	if err != nil {
		return err
	}
	if file.State == DeliveryConfirmed || file.Attempts == 0 || receiptID == "" || len(receiptID) > 256 || strings.ContainsRune(receiptID, 0) {
		return ErrInvalidManifest
	}
	file.State = DeliveryConfirmed
	file.ReceiptID = receiptID
	file.FailureCode = ""
	return nil
}

// Retryable returns copies of files without confirmed delivery.
func (m DeliveryManifest) Retryable() []DeliveryFile {
	result := make([]DeliveryFile, 0, len(m.Files))
	for _, file := range m.Files {
		if file.State != DeliveryConfirmed {
			result = append(result, file)
		}
	}
	return result
}

// Validate checks persisted manifest invariants after journal replay.
func (m DeliveryManifest) Validate() error {
	if m.FinalID == "" || strings.ContainsRune(m.FinalID, 0) {
		return ErrInvalidManifest
	}
	seenIDs := make(map[string]struct{}, len(m.Files))
	seenPaths := make(map[string]struct{}, len(m.Files))
	for _, file := range m.Files {
		if !filepath.IsAbs(file.Path) || file.Path != filepath.Clean(file.Path) || file.FileID != stableFileID(m.FinalID, file.Path) {
			return ErrInvalidManifest
		}
		if _, exists := seenIDs[file.FileID]; exists {
			return ErrInvalidManifest
		}
		if _, exists := seenPaths[file.Path]; exists {
			return ErrInvalidManifest
		}
		seenIDs[file.FileID] = struct{}{}
		seenPaths[file.Path] = struct{}{}
		switch file.State {
		case DeliveryPending:
			if file.ReceiptID != "" || file.FailureCode != "" {
				return ErrInvalidManifest
			}
		case DeliveryUnconfirmed:
			if file.Attempts == 0 || file.ReceiptID != "" || !failureCodePattern.MatchString(file.FailureCode) {
				return ErrInvalidManifest
			}
		case DeliveryConfirmed:
			if file.Attempts == 0 || file.ReceiptID == "" || len(file.ReceiptID) > 256 || strings.ContainsRune(file.ReceiptID, 0) || file.FailureCode != "" {
				return ErrInvalidManifest
			}
		default:
			return ErrInvalidManifest
		}
	}
	return nil
}

func (m *DeliveryManifest) find(fileID string) (*DeliveryFile, error) {
	if m == nil {
		return nil, ErrInvalidManifest
	}
	for index := range m.Files {
		if m.Files[index].FileID == fileID {
			return &m.Files[index], nil
		}
	}
	return nil, ErrUnknownFile
}

func stableFileID(finalID, path string) string {
	hash := sha256.Sum256([]byte(finalID + "\x00" + path))
	return hex.EncodeToString(hash[:16])
}
