package sessioncreation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"bria/internal/domain"
)

var (
	ErrUnavailablePath = errors.New("session creation path is unavailable")
	ErrInvalidName     = errors.New("session creation directory name is invalid")
)

type ProviderCapability struct {
	Provider  domain.Provider
	Installed bool
	Enabled   bool
}

type Computer struct {
	ID           domain.ComputerID
	Name         string
	Capabilities []ProviderCapability
	Coordinator  bool
	Available    bool
}

type Directory struct {
	Name string
	Path string
}

// Environment is the node-owned, provider-neutral surface required by the
// creation flow. Network compositions can implement it without teaching the
// Telegram controller about a transport protocol.
type Environment interface {
	AvailableComputers(context.Context) ([]Computer, error)
	Roots(context.Context, domain.ComputerID) ([]Directory, error)
	Browse(context.Context, domain.ComputerID, string) ([]Directory, error)
	CreateChild(context.Context, domain.ComputerID, string, string) (string, error)
	Parent(context.Context, domain.ComputerID, string) (string, bool)
}

// Inventory optionally exposes registered computers which are currently
// unavailable. Environment.AvailableComputers remains the authoritative live
// readiness check used before creation.
type Inventory interface {
	RegisteredComputers(context.Context) ([]Computer, error)
}

// ProviderActivator changes an installed provider on its owning node.
type ProviderActivator interface {
	ToggleProvider(context.Context, domain.ComputerID, domain.Provider) error
}

type CapabilitySource func(context.Context) ([]ProviderCapability, error)

// LocalEnvironment combines one responsive local computer with the same
// filesystem operations used by a future remote executor implementation.
type LocalEnvironment struct {
	browser      *LocalBrowser
	computer     Computer
	capabilities CapabilitySource
}

func NewLocalEnvironment(computerID domain.ComputerID, computerName string, roots []string, capabilities CapabilitySource) (*LocalEnvironment, error) {
	name := strings.TrimSpace(computerName)
	if name == "" {
		name = string(computerID)
	}
	browser, err := NewLocalBrowser(computerID, roots)
	if err != nil || capabilities == nil {
		return nil, ErrUnavailablePath
	}
	return &LocalEnvironment{browser: browser, computer: Computer{ID: computerID, Name: name}, capabilities: capabilities}, nil
}

func (environment *LocalEnvironment) AvailableComputers(ctx context.Context) ([]Computer, error) {
	if environment == nil || environment.browser == nil || environment.capabilities == nil {
		return nil, ErrUnavailablePath
	}
	capabilities, err := environment.capabilities(ctx)
	if err != nil {
		return nil, err
	}
	computer := environment.computer
	computer.Capabilities = append([]ProviderCapability(nil), capabilities...)
	computer.Coordinator = true
	computer.Available = true
	return []Computer{computer}, nil
}

func (environment *LocalEnvironment) RegisteredComputers(ctx context.Context) ([]Computer, error) {
	return environment.AvailableComputers(ctx)
}

func (environment *LocalEnvironment) Roots(ctx context.Context, computerID domain.ComputerID) ([]Directory, error) {
	return environment.browser.Roots(ctx, computerID)
}

func (environment *LocalEnvironment) Browse(ctx context.Context, computerID domain.ComputerID, path string) ([]Directory, error) {
	return environment.browser.Browse(ctx, computerID, path)
}

func (environment *LocalEnvironment) CreateChild(ctx context.Context, computerID domain.ComputerID, parent, name string) (string, error) {
	return environment.browser.CreateChild(ctx, computerID, parent, name)
}

func (environment *LocalEnvironment) Parent(ctx context.Context, computerID domain.ComputerID, path string) (string, bool) {
	if err := environment.browser.validate(ctx, computerID); err != nil {
		return "", false
	}
	return environment.browser.Parent(path)
}

// LocalBrowser implements the filesystem half of Environment for one local
// computer. Provider capability discovery remains composition-owned.
type LocalBrowser struct {
	computerID domain.ComputerID
	roots      []Directory
}

func NewLocalBrowser(computerID domain.ComputerID, configuredRoots []string) (*LocalBrowser, error) {
	if strings.TrimSpace(string(computerID)) == "" {
		return nil, ErrUnavailablePath
	}
	if len(configuredRoots) == 0 {
		configuredRoots = platformRoots()
	}
	roots := make([]Directory, 0, len(configuredRoots))
	seen := make(map[string]struct{}, len(configuredRoots))
	for _, raw := range configuredRoots {
		canonical, err := canonicalDirectory(raw)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		label := canonical
		if volume := filepath.VolumeName(canonical); volume != "" {
			label = volume + string(os.PathSeparator)
		}
		roots = append(roots, Directory{Name: label, Path: canonical})
	}
	if len(roots) == 0 {
		return nil, ErrUnavailablePath
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Path < roots[j].Path })
	return &LocalBrowser{computerID: computerID, roots: roots}, nil
}

func (browser *LocalBrowser) Roots(ctx context.Context, computerID domain.ComputerID) ([]Directory, error) {
	if err := browser.validate(ctx, computerID); err != nil {
		return nil, err
	}
	return append([]Directory(nil), browser.roots...), nil
}

func (browser *LocalBrowser) Browse(ctx context.Context, computerID domain.ComputerID, path string) ([]Directory, error) {
	if err := browser.validate(ctx, computerID); err != nil {
		return nil, err
	}
	canonical, _, err := browser.allowedDirectory(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(canonical)
	if err != nil {
		return nil, ErrUnavailablePath
	}
	directories := make([]Directory, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		child, _, childErr := browser.allowedDirectory(filepath.Join(canonical, entry.Name()))
		if childErr != nil {
			continue
		}
		directories = append(directories, Directory{Name: entry.Name(), Path: child})
	}
	sort.SliceStable(directories, func(i, j int) bool {
		return strings.ToLower(directories[i].Name) < strings.ToLower(directories[j].Name)
	})
	return directories, nil
}

func (browser *LocalBrowser) CreateChild(ctx context.Context, computerID domain.ComputerID, parent, name string) (string, error) {
	if err := browser.validate(ctx, computerID); err != nil {
		return "", err
	}
	if !validChildName(name) {
		return "", ErrInvalidName
	}
	canonicalParent, _, err := browser.allowedDirectory(parent)
	if err != nil {
		return "", err
	}
	target := filepath.Join(canonicalParent, name)
	if info, statErr := os.Lstat(target); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrInvalidName
		}
		canonical, _, allowedErr := browser.allowedDirectory(target)
		return canonical, allowedErr
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", ErrUnavailablePath
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return "", ErrUnavailablePath
	}
	canonical, _, err := browser.allowedDirectory(target)
	return canonical, err
}

func (browser *LocalBrowser) Parent(path string) (string, bool) {
	canonical, root, err := browser.allowedDirectory(path)
	if err != nil || canonical == root {
		return "", false
	}
	parent := filepath.Dir(canonical)
	if !within(root, parent) {
		return "", false
	}
	return parent, true
}

func (browser *LocalBrowser) validate(ctx context.Context, computerID domain.ComputerID) error {
	if browser == nil || computerID != browser.computerID {
		return ErrUnavailablePath
	}
	return ctx.Err()
}

func (browser *LocalBrowser) allowedDirectory(path string) (string, string, error) {
	canonical, err := canonicalDirectory(path)
	if err != nil {
		return "", "", err
	}
	for _, root := range browser.roots {
		if within(root.Path, canonical) {
			return canonical, root.Path, nil
		}
	}
	return "", "", ErrUnavailablePath
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || strings.TrimSpace(path) != path || strings.ContainsRune(path, '\x00') {
		return "", ErrUnavailablePath
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil || !filepath.IsAbs(canonical) {
		return "", ErrUnavailablePath
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", ErrUnavailablePath
	}
	return canonical, nil
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func validChildName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) &&
		strings.TrimSpace(name) == name && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func platformRoots() []string {
	if runtime.GOOS != "windows" {
		return []string{string(os.PathSeparator)}
	}
	volume := filepath.VolumeName(mustWorkingDirectory())
	if volume == "" {
		return nil
	}
	return []string{volume + string(os.PathSeparator)}
}

func mustWorkingDirectory() string {
	workdir, _ := os.Getwd()
	return workdir
}
