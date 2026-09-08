// Package nativescreencache owns guarded local screenshot rendering, immutable
// PNG identities, receipt reuse, and lifecycle invalidation. The transport owns
// refresh cadence; runtime snapshots and preferences arrive through callbacks.
package nativescreencache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"bria/internal/domain"
	"bria/internal/nativerender"
)

const (
	maxCachedNativePNGsPerSession = 3
	maxRichNativePNGBytes         = 1 << 20
)

// NativeImage is the exact rendered screenshot identity used by Telegram.
// FileID is present only after Telegram confirmed that exact PNG hash.
type NativeImage struct {
	PNG    []byte
	Hash   string
	FileID string
}

type nativeCacheEntry struct {
	snapshotHash string
	captureKiB   int
	image        NativeImage
}

var ErrInvalidConfiguration = errors.New("screen production configuration is invalid")

// Preferences selects whether and how much of the terminal may be rendered.
type Preferences struct {
	ScreenEnabled         bool
	ScreenCaptureLimitKiB int
}

// Snapshot is the exact native terminal identity, independent of provider types.
type Snapshot struct {
	FullText string
	Hash     string
}

// Config supplies current preferences and active/native session views.
// Callbacks may be called by background refresh and must support concurrency.
type Config struct {
	Preferences   func(context.Context) (Preferences, error)
	ActiveSession func(context.Context) (domain.SessionID, error)
	Snapshot      func(domain.SessionID) (Snapshot, bool)
}

type Source struct {
	preferences    func(context.Context) (Preferences, error)
	activeSession  func(context.Context) (domain.SessionID, error)
	snapshot       func(domain.SessionID) (Snapshot, bool)
	mu             sync.Mutex
	session        domain.SessionID
	hash           string // native snapshot hash of the current ready image
	pngHash        string
	fileID         string
	png            []byte
	cache          map[domain.SessionID][]nativeCacheEntry
	epoch          map[domain.SessionID]uint64
	inFlight       bool
	requestPending bool
}

func New(config Config) (*Source, error) {
	if config.Preferences == nil || config.ActiveSession == nil || config.Snapshot == nil {
		return nil, ErrInvalidConfiguration
	}
	return &Source{
		preferences: config.Preferences, activeSession: config.ActiveSession, snapshot: config.Snapshot,
		cache: make(map[domain.SessionID][]nativeCacheEntry),
		epoch: make(map[domain.SessionID]uint64),
	}, nil
}

// CachedScreenPNG returns only the most recently completed image for the
// requested session. It never reads the live process or performs rendering.
func (source *Source) CachedScreenPNG(sessionID string) []byte {
	return source.CachedScreen(sessionID).PNG
}

// CachedScreenDelivery is the dependency-neutral bridge contract for one
// ready image. A non-empty FileID permits rich_md to reuse Telegram media
// without uploading PNG bytes again.
func (source *Source) CachedScreenDelivery(sessionID string) (png []byte, hash, fileID string) {
	image := source.CachedScreen(sessionID)
	return image.PNG, image.Hash, image.FileID
}

// CachedScreen returns only an already-completed image and never touches the
// provider runtime. It is suitable for the rich_md delivery critical path.
func (source *Source) CachedScreen(sessionID string) NativeImage {
	if source == nil || sessionID == "" {
		return NativeImage{}
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.session != domain.SessionID(sessionID) || len(source.png) == 0 {
		return NativeImage{}
	}
	return cloneNativeImage(NativeImage{PNG: source.png, Hash: source.pngHash, FileID: source.fileID})
}

// RememberTelegramFileID binds a confirmed Telegram reference only to the
// exact final PNG hash that was sent. A stale receipt is ignored.
func (source *Source) RememberTelegramFileID(sessionID, pngHash, fileID string) bool {
	if source == nil || sessionID == "" || pngHash == "" || fileID == "" {
		return false
	}
	id := domain.SessionID(sessionID)
	source.mu.Lock()
	defer source.mu.Unlock()
	entries := source.cache[id]
	for index := range entries {
		if entries[index].image.Hash != pngHash {
			continue
		}
		entries[index].image.FileID = fileID
		source.cache[id] = entries
		if source.session == id && source.pngHash == pngHash {
			source.fileID = fileID
		}
		return true
	}
	return false
}

// ForgetSession clears all PNG bytes and Telegram references for an archived
// or deleted session. The epoch prevents an in-flight render from restoring
// the forgotten cache after lifecycle completion.
func (source *Source) ForgetSession(sessionID string) {
	if source == nil || sessionID == "" {
		return
	}
	id := domain.SessionID(sessionID)
	source.mu.Lock()
	delete(source.cache, id)
	source.epoch[id]++
	if source.session == id {
		source.session, source.hash, source.pngHash, source.fileID, source.png = "", "", "", "", nil
	}
	source.mu.Unlock()
}

// RequestScreenPNG schedules a best-effort refresh. It deliberately does not
// wait for settings, runtime capture, or PNG encoding: the caller's text and
// keyboard update must remain independent from screenshot production.
func (source *Source) RequestScreenPNG(ctx context.Context, sessionID string) error {
	if source == nil || ctx == nil || sessionID == "" {
		return ErrInvalidConfiguration
	}
	source.mu.Lock()
	if source.requestPending {
		source.mu.Unlock()
		return nil
	}
	source.requestPending = true
	source.mu.Unlock()
	go source.refreshAsync(context.WithoutCancel(ctx), domain.SessionID(sessionID))
	return nil
}

func (source *Source) refreshAsync(ctx context.Context, id domain.SessionID) {
	defer func() {
		source.mu.Lock()
		source.requestPending = false
		source.mu.Unlock()
	}()
	_, _, _, _ = source.CurrentScreenDelivery(ctx, string(id))
}

// CurrentScreenDelivery renders the latest local native snapshot for this
// update. The transport owns cadence; no provider capture, timer or input is
// triggered here. Busy renders degrade to text instead of waiting indefinitely.
func (source *Source) CurrentScreenDelivery(ctx context.Context, sessionID string) ([]byte, string, string, error) {
	if source == nil || ctx == nil || sessionID == "" {
		return nil, "", "", ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}
	id := domain.SessionID(sessionID)
	source.mu.Lock()
	if source.inFlight {
		source.mu.Unlock()
		return nil, "", "", nil
	}
	source.inFlight = true
	epoch := source.epoch[id]
	source.mu.Unlock()
	defer func() { source.mu.Lock(); source.inFlight = false; source.mu.Unlock() }()

	preferences, err := source.preferences(ctx)
	if err != nil || !preferences.ScreenEnabled {
		return nil, "", "", err
	}
	active, err := source.activeSession(ctx)
	if err != nil || active != id {
		return nil, "", "", err
	}
	snapshot, ok := source.snapshot(id)
	if !ok || snapshot.FullText == "" || snapshot.Hash == "" {
		return nil, "", "", nil
	}
	source.mu.Lock()
	image, cached := source.cachedBySnapshotHash(id, snapshot.Hash, preferences.ScreenCaptureLimitKiB)
	source.mu.Unlock()
	if !cached {
		data, renderErr := nativerender.RenderNativeWithOptions(ctx, snapshot.FullText, nativerender.NativeOptions{CaptureKiB: preferences.ScreenCaptureLimitKiB})
		if renderErr != nil {
			return nil, "", "", renderErr
		}
		if len(data) == 0 || len(data) > maxRichNativePNGBytes {
			return nil, "", "", nil
		}
		source.mu.Lock()
		image = source.imageForRenderedPNG(id, data)
		source.mu.Unlock()
	}
	// User navigation, Screen-off, or Forget may happen while encoding.
	active, err = source.activeSession(ctx)
	if err != nil || active != id {
		return nil, "", "", err
	}
	currentPreferences, err := source.preferences(ctx)
	if err != nil || !currentPreferences.ScreenEnabled || currentPreferences.ScreenCaptureLimitKiB != preferences.ScreenCaptureLimitKiB {
		return nil, "", "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.epoch[id] != epoch {
		return nil, "", "", nil
	}
	// Preserve a receipt arriving during the render/checks.
	if prior, ok := source.cachedByPNGHash(id, image.Hash); ok {
		image.FileID = prior.FileID
	}
	source.remember(id, nativeCacheEntry{snapshotHash: snapshot.Hash, captureKiB: preferences.ScreenCaptureLimitKiB, image: image})
	source.setCurrent(id, snapshot.Hash, image)
	return append([]byte(nil), image.PNG...), image.Hash, image.FileID, nil
}

// ScreenPNG keeps the existing synchronous API on the guarded delivery path.
func (source *Source) ScreenPNG(ctx context.Context, sessionID string) ([]byte, error) {
	png, _, _, err := source.CurrentScreenDelivery(ctx, sessionID)
	return png, err
}

func (source *Source) cachedBySnapshotHash(id domain.SessionID, hash string, captureKiB int) (NativeImage, bool) {
	for _, entry := range source.cache[id] {
		if entry.snapshotHash == hash && entry.captureKiB == captureKiB && len(entry.image.PNG) != 0 {
			return entry.image, true
		}
	}
	return NativeImage{}, false
}

func (source *Source) cachedByPNGHash(id domain.SessionID, hash string) (NativeImage, bool) {
	for _, entry := range source.cache[id] {
		if entry.image.Hash == hash && len(entry.image.PNG) != 0 {
			return entry.image, true
		}
	}
	return NativeImage{}, false
}

func (source *Source) imageForRenderedPNG(id domain.SessionID, data []byte) NativeImage {
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	image := NativeImage{PNG: append([]byte(nil), data...), Hash: hash}
	if cached, ok := source.cachedByPNGHash(id, hash); ok {
		image.FileID = cached.FileID
	}
	return image
}

func (source *Source) remember(id domain.SessionID, entry nativeCacheEntry) {
	entries := source.cache[id]
	filtered := make([]nativeCacheEntry, 0, maxCachedNativePNGsPerSession)
	filtered = append(filtered, entry)
	for _, cached := range entries {
		if cached.snapshotHash == entry.snapshotHash && cached.captureKiB == entry.captureKiB || cached.image.Hash == entry.image.Hash {
			continue
		}
		filtered = append(filtered, cached)
		if len(filtered) == maxCachedNativePNGsPerSession {
			break
		}
	}
	source.cache[id] = filtered
}

func (source *Source) setCurrent(id domain.SessionID, snapshotHash string, image NativeImage) {
	source.session, source.hash = id, snapshotHash
	source.pngHash, source.fileID = image.Hash, image.FileID
	source.png = append([]byte(nil), image.PNG...)
}

func cloneNativeImage(image NativeImage) NativeImage {
	image.PNG = append([]byte(nil), image.PNG...)
	return image
}
