// Package terminalbinding persists exact terminal identity and exclusive observer
// ownership. Provider names are opaque data; it never starts or resumes a CLI.
package terminalbinding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"bria/internal/instancelock"
)

var (
	ErrMismatch    = errors.New("native terminal binding mismatch")
	ErrUnavailable = errors.New("native terminal unavailable")
	ErrNotManaged  = fmt.Errorf("native terminal not managed: %w", ErrUnavailable)
	ErrOwned       = errors.New("native terminal observer already attached")
)

type Identity struct{ LogicalSessionID, NativeSessionID, Provider, Workdir string }

type Record struct {
	Version                   int
	Identity                  Identity
	Socket, Nonce, PaneID     string
	SocketDevice, SocketInode uint64
	ServerPID, PanePID        int
	ServerBirth, PaneBirth    string
	LiteralInputBarrier       bool
}

// Lease is held by exactly one observer. The lock file is never unlinked.
type Lease struct {
	path string
	lock *instancelock.Lock
}

func Canonical(identity Identity) (Identity, error) {
	for _, value := range []string{identity.LogicalSessionID, identity.NativeSessionID, identity.Provider} {
		if value == "" || len(value) > 256 || strings.TrimSpace(value) != value ||
			!utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return Identity{}, ErrMismatch
		}
	}
	if !filepath.IsAbs(identity.Workdir) {
		return Identity{}, ErrMismatch
	}
	workdir, err := filepath.EvalSymlinks(identity.Workdir)
	if err != nil {
		return Identity{}, ErrMismatch
	}
	info, err := os.Stat(workdir)
	if err != nil || !info.IsDir() {
		return Identity{}, ErrMismatch
	}
	identity.Workdir = workdir
	return identity, nil
}

func Acquire(stateDir string, identity Identity, create bool) (*Lease, error) {
	canonical, err := Canonical(identity)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(stateDir) {
		return nil, ErrMismatch
	}
	root, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return nil, missingBinding(stateDir, err)
	}
	root = filepath.Join(root, "terminal-bindings")
	if create {
		if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, ErrUnavailable
		}
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, missingBinding(root, err)
	}
	if !private(info, true) {
		return nil, ErrMismatch
	}
	key, _ := json.Marshal([]string{canonical.Provider, canonical.NativeSessionID})
	hash := sha256.Sum256(key)
	path := filepath.Join(root, hex.EncodeToString(hash[:])+".json")
	if !create {
		if _, err := os.Lstat(path); err != nil {
			return nil, missingBinding(path, err)
		}
	}
	lock, err := instancelock.Acquire(path)
	if errors.Is(err, instancelock.ErrAlreadyLocked) {
		return nil, ErrOwned
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Lease{path: path, lock: lock}, nil
}

// Absence permits explicit resume only beneath an owned non-writable directory.
// Dangling symlinks, denied access and untrusted ancestors are not absence.
func missingBinding(path string, err error) error {
	if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	for depth := 0; depth < 32; depth++ {
		info, err := os.Lstat(path)
		if err == nil && trustedDirectory(info) {
			return ErrNotManaged
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ErrUnavailable
		}
		path = filepath.Dir(path)
	}
	return ErrUnavailable
}

func (l *Lease) Close() error { return l.lock.Close() }

func (l *Lease) Read(expected Identity) (Record, error) {
	var record Record
	info, err := os.Lstat(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return record, ErrUnavailable
	}
	if err != nil || !private(info, false) || info.Size() > 8192 {
		return record, ErrMismatch
	}
	file, err := os.Open(l.path)
	if err != nil {
		return record, ErrUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return record, ErrMismatch
	}
	decoder := json.NewDecoder(io.LimitReader(file, 8193))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF {
		return record, ErrMismatch
	}
	canonical, err := Canonical(expected)
	if err != nil || record.Version != 1 || record.Identity != canonical ||
		record.ServerPID < 2 || record.PanePID < 2 || record.ServerBirth == "" || record.PaneBirth == "" ||
		len(record.Nonce) != 64 || record.PaneID != "%0" {
		return Record{}, ErrMismatch
	}
	if _, err := hex.DecodeString(record.Nonce); err != nil {
		return Record{}, ErrMismatch
	}
	return record, nil
}

// Save never overwrites an existing binding, even if its CLI appears gone.
func (l *Lease) Save(record Record) error {
	if _, err := os.Lstat(l.path); !errors.Is(err, os.ErrNotExist) {
		return ErrMismatch
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > 8192 {
		return ErrMismatch
	}
	file, err := os.CreateTemp(filepath.Dir(l.path), ".binding-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), l.path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(l.path))
}

// Remove is reserved for an explicitly closed, verified terminal.
func (l *Lease) Remove() error {
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Dir(l.path))
}

func syncDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
