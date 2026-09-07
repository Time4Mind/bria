package screenproduction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"bria/internal/domain"
	"bria/internal/screen"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
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
	image        NativeImage
}

type ActiveSessionReader interface {
	LoadActiveSession(context.Context) (domain.SessionID, error)
}

type NativeSource struct {
	settings       settings.Store
	sessions       ActiveSessionReader
	runtime        sessionruntime.NativeScreenProvider
	mu             sync.Mutex
	session        domain.SessionID
	hash           string // native snapshot hash of the current ready image
	pngHash        string
	fileID         string
	png            []byte
	cache          map[domain.SessionID][]nativeCacheEntry
	epoch          map[domain.SessionID]uint64
	lastCapture    time.Time
	interval       time.Duration
	captures       int
	inFlight       bool
	requestPending bool
}

// ScreenshotRefreshSource is implemented by the production native source.
// A rich-card delivery can consume the last ready image and request the next
// one without putting PNG rendering on the text-card critical path.
type ScreenshotRefreshSource interface {
	CachedScreenPNG(string) []byte
	RequestScreenPNG(context.Context, string) error
}

const (
	minScreenshotInterval    = 2500 * time.Millisecond
	steadyScreenshotInterval = 3500 * time.Millisecond
	maxScreenshotInterval    = 5 * time.Second
)

func screenshotInterval(captures int) time.Duration {
	switch {
	case captures >= 30:
		return maxScreenshotInterval
	case captures >= 10:
		return steadyScreenshotInterval
	default:
		return minScreenshotInterval
	}
}

func NewNativeSource(preferences settings.Store, sessions ActiveSessionReader, runtime sessionruntime.NativeScreenProvider) (*NativeSource, error) {
	if preferences == nil || sessions == nil || runtime == nil {
		return nil, ErrInvalidConfiguration
	}
	return &NativeSource{
		settings: preferences, sessions: sessions, runtime: runtime,
		cache: make(map[domain.SessionID][]nativeCacheEntry),
		epoch: make(map[domain.SessionID]uint64), interval: minScreenshotInterval,
	}, nil
}

// CachedScreenPNG returns only the most recently completed image for the
// requested session. It never reads the live process or performs rendering.
func (source *NativeSource) CachedScreenPNG(sessionID string) []byte {
	return source.CachedScreen(sessionID).PNG
}

// CachedScreenDelivery is the dependency-neutral bridge contract for one
// ready image. A non-empty FileID permits rich_md to reuse Telegram media
// without uploading PNG bytes again.
func (source *NativeSource) CachedScreenDelivery(sessionID string) (png []byte, hash, fileID string) {
	image := source.CachedScreen(sessionID)
	return image.PNG, image.Hash, image.FileID
}

// CachedScreen returns only an already-completed image and never touches the
// provider runtime. It is suitable for the rich_md delivery critical path.
func (source *NativeSource) CachedScreen(sessionID string) NativeImage {
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
func (source *NativeSource) RememberTelegramFileID(sessionID, pngHash, fileID string) bool {
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
func (source *NativeSource) ForgetSession(sessionID string) {
	if source == nil || sessionID == "" {
		return
	}
	id := domain.SessionID(sessionID)
	source.mu.Lock()
	delete(source.cache, id)
	source.epoch[id]++
	if source.session == id {
		source.session, source.hash, source.pngHash, source.fileID, source.png = "", "", "", "", nil
		source.lastCapture, source.captures = time.Time{}, 0
		source.interval = minScreenshotInterval
	}
	source.mu.Unlock()
}

// RequestScreenPNG schedules a best-effort refresh. It deliberately does not
// wait for settings, runtime capture, or PNG encoding: the caller's text and
// keyboard update must remain independent from screenshot production.
func (source *NativeSource) RequestScreenPNG(ctx context.Context, sessionID string) error {
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

func (source *NativeSource) refreshAsync(ctx context.Context, id domain.SessionID) {
	defer func() {
		source.mu.Lock()
		source.requestPending = false
		source.mu.Unlock()
	}()
	preferences, err := source.settings.Load(ctx)
	if err != nil || !preferences.ScreenEnabled {
		return
	}
	active, err := source.sessions.LoadActiveSession(ctx)
	if err != nil || active != id {
		return
	}
	snapshot, ok := source.runtime.NativeScreen(id)
	if !ok || snapshot.FullText == "" || snapshot.Hash == "" {
		return
	}
	now := time.Now()
	source.mu.Lock()
	if source.session != id {
		source.session, source.hash, source.pngHash, source.fileID, source.png = id, "", "", "", nil
		source.lastCapture, source.captures = time.Time{}, 0
		source.interval = minScreenshotInterval
	}
	if cached, ok := source.cachedBySnapshotHash(id, snapshot.Hash); ok {
		source.setCurrent(id, snapshot.Hash, cached)
		source.mu.Unlock()
		return
	}
	if source.hash == snapshot.Hash && len(source.png) > 0 {
		source.mu.Unlock()
		return
	}
	if source.inFlight || (!source.lastCapture.IsZero() && now.Sub(source.lastCapture) < source.interval) {
		source.mu.Unlock()
		return
	}
	source.inFlight = true
	epoch := source.epoch[id]
	source.mu.Unlock()

	data, renderErr := screen.RenderNativeWithOptions(ctx, snapshot.FullText, screen.NativeOptions{CaptureKiB: preferences.ScreenCaptureLimitKiB})
	active, activeErr := source.sessions.LoadActiveSession(ctx)
	source.mu.Lock()
	defer source.mu.Unlock()
	source.inFlight = false
	if renderErr != nil || activeErr != nil || active != id || len(data) == 0 ||
		len(data) > maxRichNativePNGBytes || source.epoch[id] != epoch {
		return
	}
	image := source.imageForRenderedPNG(id, data)
	source.remember(id, nativeCacheEntry{snapshotHash: snapshot.Hash, image: image})
	source.setCurrent(id, snapshot.Hash, image)
	source.lastCapture = time.Now()
	source.captures++
	source.interval = screenshotInterval(source.captures)
}

// ScreenPNG is active-card only, globally opt-in, and strictly cache-based.
// Unavailable snapshots are not substituted with old event-transcript images.
func (source *NativeSource) ScreenPNG(ctx context.Context, sessionID string) ([]byte, error) {
	id := domain.SessionID(sessionID)
	if source == nil || ctx == nil || id == "" {
		return nil, ErrInvalidConfiguration
	}
	preferences, err := source.settings.Load(ctx)
	if err != nil {
		return nil, err
	}
	if !preferences.ScreenEnabled {
		return nil, nil
	}
	active, err := source.sessions.LoadActiveSession(ctx)
	if err != nil {
		return nil, err
	}
	if active != id {
		return nil, nil
	}
	snapshot, ok := source.runtime.NativeScreen(id)
	if !ok || snapshot.FullText == "" {
		return nil, nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.session != id {
		source.session, source.hash, source.pngHash, source.fileID, source.png = id, "", "", "", nil
		source.lastCapture, source.captures = time.Time{}, 0
		source.interval = minScreenshotInterval
	}
	if source.session != id || source.hash != snapshot.Hash || len(source.png) == 0 {
		if cached, ok := source.cachedBySnapshotHash(id, snapshot.Hash); ok {
			source.setCurrent(id, snapshot.Hash, cached)
		} else {
			png, err := screen.RenderNativeWithOptions(ctx, snapshot.FullText, screen.NativeOptions{CaptureKiB: preferences.ScreenCaptureLimitKiB})
			if err != nil {
				return nil, err
			}
			// An unexpectedly oversized image must not block the rich text card.
			// The renderer owns normal content cropping to the accepted bound.
			if len(png) > maxRichNativePNGBytes {
				return nil, nil
			}
			image := source.imageForRenderedPNG(id, png)
			source.remember(id, nativeCacheEntry{snapshotHash: snapshot.Hash, image: image})
			source.setCurrent(id, snapshot.Hash, image)
			source.lastCapture = time.Now()
			source.captures++
			source.interval = screenshotInterval(source.captures)
		}
	}
	// A switch during rendering must not attach the former node's terminal.
	active, err = source.sessions.LoadActiveSession(ctx)
	if err != nil {
		return nil, err
	}
	if active != id {
		return nil, nil
	}
	return append([]byte(nil), source.png...), nil
}

func (source *NativeSource) cachedBySnapshotHash(id domain.SessionID, hash string) (NativeImage, bool) {
	for _, entry := range source.cache[id] {
		if entry.snapshotHash == hash && len(entry.image.PNG) != 0 {
			return entry.image, true
		}
	}
	return NativeImage{}, false
}

func (source *NativeSource) cachedByPNGHash(id domain.SessionID, hash string) (NativeImage, bool) {
	for _, entry := range source.cache[id] {
		if entry.image.Hash == hash && len(entry.image.PNG) != 0 {
			return entry.image, true
		}
	}
	return NativeImage{}, false
}

func (source *NativeSource) imageForRenderedPNG(id domain.SessionID, data []byte) NativeImage {
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	image := NativeImage{PNG: append([]byte(nil), data...), Hash: hash}
	if cached, ok := source.cachedByPNGHash(id, hash); ok {
		image.FileID = cached.FileID
	}
	return image
}

func (source *NativeSource) remember(id domain.SessionID, entry nativeCacheEntry) {
	entries := source.cache[id]
	filtered := make([]nativeCacheEntry, 0, maxCachedNativePNGsPerSession)
	filtered = append(filtered, entry)
	for _, cached := range entries {
		if cached.snapshotHash == entry.snapshotHash || cached.image.Hash == entry.image.Hash {
			continue
		}
		filtered = append(filtered, cached)
		if len(filtered) == maxCachedNativePNGsPerSession {
			break
		}
	}
	source.cache[id] = filtered
}

func (source *NativeSource) setCurrent(id domain.SessionID, snapshotHash string, image NativeImage) {
	source.session, source.hash = id, snapshotHash
	source.pngHash, source.fileID = image.Hash, image.FileID
	source.png = append([]byte(nil), image.PNG...)
}

func cloneNativeImage(image NativeImage) NativeImage {
	image.PNG = append([]byte(nil), image.PNG...)
	return image
}
