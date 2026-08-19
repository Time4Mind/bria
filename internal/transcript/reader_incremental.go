package transcript

import (
	"fmt"
	"os"
)

// readAppended extends a verified cache entry from an append-only JSONL tail.
// It avoids reparsing the full bounded suffix on every provider write while
// retaining the configured number of recent events as the byte window moves.
func (r *Reader) readAppended(
	path string,
	info os.FileInfo,
	backend Backend,
) ([]Event, int, int, bool, error) {
	entry, ok := r.cachedReadBase(path)
	if !ok || entry.info == nil || !os.SameFile(entry.info, info) ||
		entry.size <= 0 || info.Size() <= entry.size ||
		info.Size()-entry.size > r.config.MaxReadBytes {
		return nil, 0, 0, false, nil
	}
	lines, complete, err := readAppendedJSONLLines(
		path, entry.size, info.Size()-entry.size, r.config.MaxLineBytes,
	)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("read appended transcript: %w", err)
	}
	if !complete {
		return nil, 0, 0, false, nil
	}
	added, parsed := parseRecentEvents(
		backend, lines, r.config.MaxBodyBytes, r.config.MaxEvents,
	)
	events := append(cloneEvents(entry.events), added...)
	if visibleEventCount(events) > r.config.MaxEvents {
		events = cloneEvents(trimRecentVisibleEvents(events, r.config.MaxEvents))
	}
	return events, len(lines), parsed, true, nil
}

func (r *Reader) cachedReadBase(path string) (readCacheEntry, bool) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	entry, ok := r.readCache[path]
	if !ok {
		return readCacheEntry{}, false
	}
	entry.events = cloneEvents(entry.events)
	return entry, true
}
