// Package files owns safe local file staging and outbound file resolution.
package files

import (
	"errors"
	"io"
	"os"
)

var (
	ErrInvalidLimit  = errors.New("file size limit must be positive")
	ErrInvalidSource = errors.New("file source is nil")
	ErrTooLarge      = errors.New("file exceeds size limit")
)

// Stager stores an untrusted stream in a service-owned temporary file.
type Stager struct {
	Directory string
	MaxBytes  int64
}

// Temporary is a staged file. Cleanup is safe to call more than once.
type Temporary struct {
	path string
}

// Stage stores src without using any remote-supplied filename.
func (s Stager) Stage(src io.Reader) (*Temporary, error) {
	if s.MaxBytes <= 0 {
		return nil, ErrInvalidLimit
	}
	if src == nil {
		return nil, ErrInvalidSource
	}

	file, err := os.CreateTemp(s.Directory, "bria-media-*")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()

	written, err := io.Copy(file, io.LimitReader(src, s.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if written > s.MaxBytes {
		return nil, ErrTooLarge
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	keep = true
	return &Temporary{path: path}, nil
}

// Path returns the service-generated local path. Remote filenames are never
// incorporated in this value.
func (f *Temporary) Path() string {
	if f == nil {
		return ""
	}
	return f.path
}

// Cleanup removes a staged file.
func (f *Temporary) Cleanup() error {
	if f == nil || f.path == "" {
		return nil
	}
	err := os.Remove(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
