package transcript

import (
	"fmt"
	"testing"
	"time"
)

func TestResolveCacheEvictsLeastRecentlyUsedEntries(t *testing.T) {
	reader := &Reader{resolveCache: make(map[resolveCacheKey]resolveCacheEntry)}
	oldest := resolveCacheKey{backend: BackendCodex, sessionID: "oldest", workdir: "/work"}
	reader.rememberResolve(oldest, "/transcripts/oldest")
	for index := 1; index < maxResolveCacheEntries; index++ {
		key := resolveCacheKey{
			backend: BackendCodex, sessionID: fmt.Sprintf("session-%d", index), workdir: "/work",
		}
		reader.rememberResolve(key, fmt.Sprintf("/transcripts/%d", index))
	}
	if _, _, ok := reader.cachedResolve(oldest); !ok {
		t.Fatal("recently used entry was lost before capacity was exceeded")
	}
	newest := resolveCacheKey{backend: BackendCodex, sessionID: "newest", workdir: "/work"}
	reader.rememberResolve(newest, "/transcripts/newest")

	reader.resolveMu.Lock()
	defer reader.resolveMu.Unlock()
	if len(reader.resolveCache) != maxResolveCacheEntries || len(reader.resolveOrder) != maxResolveCacheEntries {
		t.Fatalf("cache=%d order=%d", len(reader.resolveCache), len(reader.resolveOrder))
	}
	if _, ok := reader.resolveCache[oldest]; !ok {
		t.Fatal("least-recently-used eviction removed the touched entry")
	}
	firstInsertedAfterOldest := resolveCacheKey{
		backend: BackendCodex, sessionID: "session-1", workdir: "/work",
	}
	if _, ok := reader.resolveCache[firstInsertedAfterOldest]; ok {
		t.Fatal("least-recently-used entry was not evicted")
	}
}

func TestResolveCacheMissRemovesExpiredNegativeEntry(t *testing.T) {
	reader := &Reader{resolveCache: make(map[resolveCacheKey]resolveCacheEntry)}
	missing := resolveCacheKey{backend: BackendCodex, sessionID: "missing", workdir: "/work"}
	if _, _, ok := reader.cachedResolve(missing); ok {
		t.Fatal("empty cache unexpectedly returned a hit")
	}

	reader.resolveCache[missing] = resolveCacheEntry{expiresAt: time.Now().Add(-time.Second)}
	reader.resolveOrder = []resolveCacheKey{missing}
	if _, negative, ok := reader.cachedResolve(missing); ok || negative {
		t.Fatalf("expired negative entry returned path=%q negative=%v ok=%v", "", negative, ok)
	}
	if len(reader.resolveCache) != 0 || len(reader.resolveOrder) != 0 {
		t.Fatalf("expired entry was not removed: cache=%d order=%d", len(reader.resolveCache), len(reader.resolveOrder))
	}
}
