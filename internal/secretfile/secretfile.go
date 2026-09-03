// Package secretfile passes a small secret file to a callback without retaining
// its contents after the callback returns.
package secretfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// HardMaxBytes is the largest accepted physical secret file.
const HardMaxBytes = 250

var (
	ErrInvalidOptions = errors.New("invalid secret file options")
	ErrInvalidPath    = errors.New("invalid secret file path")
	ErrUnsafeFile     = errors.New("unsafe secret file")
	ErrRead           = errors.New("secret file read failed")
	ErrTooLarge       = errors.New("secret file is too large")
	ErrTooShort       = errors.New("secret is too short")
	ErrCallback       = errors.New("secret callback failed")
)

type Options struct {
	// MaxBytes bounds the physical file size and must not exceed HardMaxBytes.
	MaxBytes int
	// MinBytes applies after optional final-newline normalization.
	MinBytes         int
	TrimFinalNewline bool
}

type Callback func(secret []byte) error

// Use reads path, calls callback with a fresh buffer, then overwrites the
// complete buffer. Callback errors are intentionally replaced with ErrCallback.
// Panics propagate after the buffer has been overwritten.
func Use(path string, options Options, callback Callback) error {
	return use(path, options, callback, nil)
}

func use(path string, options Options, callback Callback, afterLstat func()) error {
	if options.MaxBytes <= 0 || options.MaxBytes > HardMaxBytes ||
		options.MinBytes <= 0 || options.MinBytes > options.MaxBytes || callback == nil {
		return ErrInvalidOptions
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidPath
	}

	before, err := os.Lstat(path)
	if err != nil || !isSecureRegular(before) || before.Size() < 0 {
		return ErrUnsafeFile
	}
	if before.Size() > int64(options.MaxBytes) {
		return ErrTooLarge
	}
	if afterLstat != nil {
		afterLstat()
	}

	file, err := os.Open(path)
	if err != nil {
		return ErrUnsafeFile
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !isSecureRegular(opened) || opened.Size() < 0 {
		_ = file.Close()
		return ErrUnsafeFile
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) || !isSecureRegular(after) {
		_ = file.Close()
		return ErrUnsafeFile
	}
	if opened.Size() > int64(options.MaxBytes) {
		_ = file.Close()
		return ErrTooLarge
	}

	buffer := make([]byte, options.MaxBytes+1)
	defer wipe(buffer)
	read, readErr := io.ReadFull(file, buffer)
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return ErrRead
	}
	if closeErr != nil {
		return ErrRead
	}
	if read > options.MaxBytes {
		return ErrTooLarge
	}

	secret := buffer[:read:read]
	if options.TrimFinalNewline && len(secret) > 0 && secret[len(secret)-1] == '\n' {
		length := len(secret) - 1
		if length > 0 && secret[length-1] == '\r' {
			length--
		}
		secret = secret[:length:length]
	}
	if len(secret) < options.MinBytes {
		return ErrTooShort
	}
	if err := callback(secret); err != nil {
		return ErrCallback
	}
	return nil
}

func isSecureRegular(info os.FileInfo) bool {
	mode := info.Mode()
	return mode.IsRegular() && mode.Perm() == 0o600 &&
		mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

func wipe(data []byte) {
	for index := range data {
		data[index] = 0
	}
	runtime.KeepAlive(data)
}
