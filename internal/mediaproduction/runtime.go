package mediaproduction

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"bria/internal/files"
	"bria/internal/mediaflow"
	"bria/internal/speech/parakeet"
)

var (
	ErrInvalidConfiguration = errors.New("media production configuration is invalid")
	ErrInvalidPhoto         = errors.New("photo custody input is invalid")
	ErrUnknownPhoto         = errors.New("photo custody reference is unknown")
	ErrReceiptMismatch      = errors.New("photo provider receipt does not match accepted custody")
	ErrPhotoCorrupt         = errors.New("photo custody content is corrupt")
	ErrInvalidTransition    = errors.New("photo custody transition is invalid")
	ErrPhotoUnavailable     = errors.New("photo custody attachment is unavailable")
)

const maxConfiguredMediaBytes int64 = 50 << 20

type DocumentMode string

const (
	DocumentsReject  DocumentMode = "reject"
	DocumentsPrepare DocumentMode = "prepare"
)

type Config struct {
	VoiceTempDirectory string
	PhotoDirectory     string
	VoiceBytes         int64
	PhotoBytes         int64
	PreparedBytes      int
	Parakeet           parakeet.Command
	DocumentMode       DocumentMode
	DocumentPolicy     mediaflow.DocumentPolicy
}

type Runtime struct {
	Preparer *mediaflow.Preparer
	Photos   *PhotoCustody
}

func Open(downloader mediaflow.Downloader, config Config) (*Runtime, error) {
	if !validBound(config.VoiceBytes) || !validBound(config.PhotoBytes) ||
		config.PreparedBytes <= 0 || config.PreparedBytes > 1<<20 {
		return nil, ErrInvalidConfiguration
	}
	voiceDirectory, err := prepareDirectory(config.VoiceTempDirectory)
	if err != nil {
		return nil, fmt.Errorf("prepare voice temporary directory: %w", err)
	}
	photos, err := OpenPhotoCustody(config.PhotoDirectory, config.PhotoBytes)
	if err != nil {
		return nil, err
	}
	var documents mediaflow.DocumentPolicy
	switch config.DocumentMode {
	case DocumentsReject:
		if !nilPort(config.DocumentPolicy) {
			return nil, ErrInvalidConfiguration
		}
	case DocumentsPrepare:
		if nilPort(config.DocumentPolicy) {
			return nil, ErrInvalidConfiguration
		}
		documents = config.DocumentPolicy
	default:
		return nil, ErrInvalidConfiguration
	}
	preparer, err := mediaflow.New(
		downloader,
		files.Stager{Directory: voiceDirectory, MaxBytes: config.VoiceBytes},
		config.Parakeet,
		photos,
		documents,
		mediaflow.Limits{VoiceBytes: config.VoiceBytes, PhotoBytes: config.PhotoBytes, PreparedBytes: config.PreparedBytes},
	)
	if err != nil {
		return nil, fmt.Errorf("compose media input preparer: %w", err)
	}
	return &Runtime{Preparer: preparer, Photos: photos}, nil
}

func validBound(value int64) bool {
	return value > 0 && value <= maxConfiguredMediaBytes
}

func prepareDirectory(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", ErrInvalidConfiguration
	}
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, volume)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	current := volume + string(filepath.Separator)
	parts := strings.Split(rest, string(filepath.Separator))
	created := false
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", ErrInvalidConfiguration
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && index == len(parts)-1 {
			if err := os.Mkdir(current, 0o700); err != nil {
				return "", err
			}
			created = true
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", ErrInvalidConfiguration
		}
	}
	info, err := os.Lstat(current)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByCurrentUser(info) {
		return "", ErrInvalidConfiguration
	}
	if created {
		if err := syncDirectory(filepath.Dir(current)); err != nil {
			return "", err
		}
	}
	return current, nil
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
