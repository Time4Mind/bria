package files

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrNoAllowedRoots = errors.New("no allowed file roots configured")
	ErrPathNotAllowed = errors.New("file path is not allowed")
	ErrNotRegular     = errors.New("path is not a regular file")
)

// Opener verifies and opens outbound files under explicit allowed roots.
type Opener struct {
	AllowedRoots []string
	MaxBytes     int64
}

// VerifiedFile remains bound to the verified file descriptor even if its path
// is replaced after OpenRegular returns.
type VerifiedFile struct {
	Path       string
	Size       int64
	readCloser io.ReadCloser
}

// Read implements io.Reader.
func (f *VerifiedFile) Read(p []byte) (int, error) { return f.readCloser.Read(p) }

// Close releases the verified file descriptor.
func (f *VerifiedFile) Close() error { return f.readCloser.Close() }

// OpenRegular opens path for streaming after applying root and file-type checks.
func (o Opener) OpenRegular(path string) (*VerifiedFile, error) {
	if len(o.AllowedRoots) == 0 {
		return nil, ErrNoAllowedRoots
	}
	if o.MaxBytes <= 0 {
		return nil, ErrInvalidLimit
	}
	if !filepath.IsAbs(path) {
		return nil, ErrPathNotAllowed
	}
	candidate := filepath.Clean(path)

	for _, configuredRoot := range o.AllowedRoots {
		root := filepath.Clean(configuredRoot)
		if !filepath.IsAbs(root) {
			continue
		}
		relative, err := filepath.Rel(root, candidate)
		if err != nil || escapesRoot(relative) {
			continue
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("resolve allowed root: %w", err)
		}
		rootInfo, err := os.Stat(resolvedRoot)
		if err != nil {
			return nil, fmt.Errorf("inspect allowed root: %w", err)
		}
		if !rootInfo.IsDir() {
			return nil, ErrPathNotAllowed
		}

		verifiedPath := filepath.Join(resolvedRoot, relative)
		before, err := inspectRegularPath(resolvedRoot, relative)
		if err != nil {
			return nil, err
		}
		file, err := openNoFollow(verifiedPath)
		if err != nil {
			return nil, fmt.Errorf("open verified file: %w", err)
		}
		keep := false
		defer func() {
			if !keep {
				_ = file.Close()
			}
		}()

		opened, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect opened file: %w", err)
		}
		if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
			return nil, ErrNotRegular
		}
		after, err := inspectRegularPath(resolvedRoot, relative)
		if err != nil || !os.SameFile(opened, after) {
			return nil, ErrPathNotAllowed
		}
		if opened.Size() > o.MaxBytes {
			return nil, ErrTooLarge
		}

		keep = true
		return &VerifiedFile{
			Path:       candidate,
			Size:       opened.Size(),
			readCloser: &boundedReadCloser{file: file, remaining: o.MaxBytes},
		}, nil
	}
	return nil, ErrPathNotAllowed
}

type boundedReadCloser struct {
	file      *os.File
	remaining int64
	exceeded  bool
}

func (r *boundedReadCloser) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if r.exceeded {
		return 0, ErrTooLarge
	}
	if r.remaining == 0 {
		var probe [1]byte
		read, err := r.file.Read(probe[:])
		if read > 0 {
			r.exceeded = true
			return 0, ErrTooLarge
		}
		return 0, err
	}
	if int64(len(destination)) > r.remaining {
		destination = destination[:r.remaining]
	}
	read, err := r.file.Read(destination)
	r.remaining -= int64(read)
	return read, err
}

func (r *boundedReadCloser) Close() error { return r.file.Close() }

func escapesRoot(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative)
}

func inspectRegularPath(root, relative string) (os.FileInfo, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect allowed root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, ErrPathNotAllowed
	}
	current := root
	for _, component := range splitPath(relative) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, fmt.Errorf("inspect file path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrPathNotAllowed
		}
	}
	info, err := os.Lstat(filepath.Join(root, relative))
	if err != nil {
		return nil, fmt.Errorf("inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	return info, nil
}

func splitPath(relative string) []string {
	if relative == "." || relative == "" {
		return nil
	}
	return strings.FieldsFunc(relative, func(r rune) bool { return r == filepath.Separator })
}
