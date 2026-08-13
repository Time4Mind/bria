package callbacktoken

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
)

func LoadFile(path string) (*Codec, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open callback key: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect callback key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != KeyBytes {
		return nil, fmt.Errorf("callback key must be a regular %d-byte file", KeyBytes)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("callback key permissions must not allow group or other access")
	}
	key := make([]byte, KeyBytes)
	if _, err := io.ReadFull(file, key); err != nil {
		return nil, fmt.Errorf("read callback key: %w", err)
	}
	return New(key)
}
