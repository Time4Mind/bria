package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"bria/internal/domain"
)

var ErrRevisionConflict = errors.New("configuration revision conflict")

// Snapshot identifies one active, validated local configuration. Its revision
// is process-local because the operational config file is changed directly;
// durable CAS belongs to user settings rather than secret-bearing config.
type Snapshot struct {
	Revision uint64
	Config   Config
}

type Store interface {
	Load(context.Context) (Config, error)
	Current(context.Context) (Snapshot, error)
	Reload(context.Context) (Snapshot, error)
	CompareAndSwap(context.Context, uint64, Config) (Snapshot, error)
	Update(context.Context, uint64, func(*Config) error) (Snapshot, error)
	SetProviderEnabled(context.Context, uint64, domain.Provider, bool) (Snapshot, error)
	LastReloadError() error
}

// FileStore keeps the last valid configuration active while making an invalid
// local edit observable to supervision and status reporting.
type FileStore struct {
	mu            sync.Mutex
	fileMu        *sync.Mutex
	path          string
	state         Snapshot
	lastReloadErr error
}

var _ Store = (*FileStore)(nil)

var configFileLocks sync.Map

func OpenFileStore(path string) (*FileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("configuration path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve configuration path: %w", err)
	}
	lock, _ := configFileLocks.LoadOrStore(absolute, &sync.Mutex{})
	fileMu := lock.(*sync.Mutex)
	fileMu.Lock()
	defer fileMu.Unlock()
	configuration, err := LoadFile(absolute)
	if err != nil {
		return nil, err
	}
	return &FileStore{fileMu: fileMu, path: configuration.sourcePath, state: Snapshot{
		Revision: 1,
		Config:   cloneConfig(configuration),
	}}, nil
}

func (store *FileStore) Load(ctx context.Context) (Config, error) {
	snapshot, err := store.Current(ctx)
	return snapshot.Config, err
}

func (store *FileStore) Current(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneSnapshot(store.state), nil
}

func (store *FileStore) Reload(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.fileMu.Lock()
	defer store.fileMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return store.reloadLocked()
}

func (store *FileStore) reloadLocked() (Snapshot, error) {
	configuration, err := LoadFile(store.path)
	if err != nil {
		store.lastReloadErr = err
		return cloneSnapshot(store.state), fmt.Errorf("reload configuration: %w", err)
	}
	if !reflect.DeepEqual(configuration, store.state.Config) {
		if store.state.Revision == math.MaxUint64 {
			store.lastReloadErr = errors.New("configuration revision overflow")
			return cloneSnapshot(store.state), store.lastReloadErr
		}
		store.state.Revision++
		store.state.Config = cloneConfig(configuration)
	}
	store.lastReloadErr = nil
	return cloneSnapshot(store.state), nil
}

func (store *FileStore) CompareAndSwap(ctx context.Context, expected uint64, next Config) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.fileMu.Lock()
	defer store.fileMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if _, err := store.reloadLocked(); err != nil {
		return Snapshot{}, err
	}
	return store.commitLocked(expected, next)
}

func (store *FileStore) Update(ctx context.Context, expected uint64, fn func(*Config) error) (Snapshot, error) {
	if fn == nil {
		return Snapshot{}, errors.New("update function is required")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.fileMu.Lock()
	defer store.fileMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if _, err := store.reloadLocked(); err != nil {
		return Snapshot{}, err
	}
	next := cloneConfig(store.state.Config)
	if err := fn(&next); err != nil {
		return Snapshot{}, err
	}
	return store.commitLocked(expected, next)
}

func (store *FileStore) SetProviderEnabled(
	ctx context.Context,
	expected uint64,
	provider domain.Provider,
	enabled bool,
) (Snapshot, error) {
	return store.Update(ctx, expected, func(configuration *Config) error {
		next, err := configuration.WithProviderEnabled(provider, enabled)
		if err != nil {
			return err
		}
		*configuration = next
		return nil
	})
}

func (store *FileStore) commitLocked(expected uint64, next Config) (Snapshot, error) {
	next.sourcePath = store.path
	if expected == math.MaxUint64 {
		return Snapshot{}, errors.New("configuration revision overflow")
	}
	if store.state.Revision != expected {
		if store.state.Revision == expected+1 && reflect.DeepEqual(store.state.Config, next) {
			return cloneSnapshot(store.state), nil
		}
		return Snapshot{}, ErrRevisionConflict
	}
	if err := next.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("validate configuration: %w", err)
	}
	if err := next.validateSourcePathCollisions(); err != nil {
		return Snapshot{}, fmt.Errorf("validate configuration paths: %w", err)
	}
	if err := writeConfigFile(store.path, next); err != nil {
		return Snapshot{}, fmt.Errorf("persist configuration: %w", err)
	}
	persisted, err := LoadFile(store.path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("reread configuration: %w", err)
	}
	if !reflect.DeepEqual(persisted, next) {
		return Snapshot{}, errors.New("reread configuration: persisted value mismatch")
	}
	store.state = Snapshot{Revision: expected + 1, Config: cloneConfig(persisted)}
	store.lastReloadErr = nil
	return cloneSnapshot(store.state), nil
}

func (store *FileStore) LastReloadError() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.lastReloadErr
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	return Snapshot{Revision: snapshot.Revision, Config: cloneConfig(snapshot.Config)}
}
func cloneConfig(configuration Config) Config {
	clone := configuration
	clone.Computer = cloneValue(configuration.Computer)
	clone.Network = cloneValue(configuration.Network)
	clone.Paths = cloneValue(configuration.Paths)
	clone.Update = cloneValue(configuration.Update)
	clone.Backup = cloneBackup(configuration.Backup)
	clone.Parakeet = cloneParakeet(configuration.Parakeet)
	clone.MediaLimits = cloneValue(configuration.MediaLimits)
	clone.Runtime = cloneRuntimeFeatures(configuration.Runtime)
	clone.Providers = make(map[string]ProviderConfig, len(configuration.Providers))
	for name, provider := range configuration.Providers {
		clonedProvider := provider
		if provider.Command != nil {
			command := *provider.Command
			command.Argv = make([]string, len(provider.Command.Argv))
			copy(command.Argv, provider.Command.Argv)
			clonedProvider.Command = &command
		}
		clone.Providers[name] = clonedProvider
	}
	return clone
}
func cloneValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
func cloneBackup(value *BackupConfig) *BackupConfig {
	if value == nil {
		return nil
	}
	clone := *value
	if value.Schedule != nil {
		schedule := *value.Schedule
		clone.Schedule = &schedule
	}
	if value.Encryption != nil {
		encryption := *value.Encryption
		clone.Encryption = &encryption
	}
	return &clone
}

func cloneParakeet(value *ParakeetConfig) *ParakeetConfig {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Argv = append([]string(nil), value.Argv...)
	if value.Argv != nil && clone.Argv == nil {
		clone.Argv = []string{}
	}
	return &clone
}

func cloneRuntimeFeatures(value *RuntimeFeatures) *RuntimeFeatures {
	if value == nil {
		return nil
	}
	return &RuntimeFeatures{
		P4:        cloneP4Runtime(value.P4),
		Discovery: cloneValue(value.Discovery),
		Backup:    cloneValue(value.Backup),
		Update:    cloneValue(value.Update),
	}
}

func cloneP4Runtime(value *P4RuntimeConfig) *P4RuntimeConfig {
	if value == nil {
		return nil
	}
	clone := *value
	clone.ArtifactAllowedRoots = append([]string(nil), value.ArtifactAllowedRoots...)
	if value.ArtifactAllowedRoots != nil && clone.ArtifactAllowedRoots == nil {
		clone.ArtifactAllowedRoots = []string{}
	}
	return &clone
}

func writeConfigFile(path string, configuration Config) error {
	data, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".config-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err == nil {
		err = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return err
}
