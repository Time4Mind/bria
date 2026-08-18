package transcript

import "os"

func sameTranscriptFileVersion(left, right os.FileInfo) bool {
	return left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func (r *Reader) cachedRead(path string, info os.FileInfo) ([]Event, bool) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	entry, ok := r.readCache[path]
	if !ok || entry.size != info.Size() || !entry.modifiedAt.Equal(info.ModTime()) {
		return nil, false
	}
	return cloneEvents(entry.events), true
}

func (r *Reader) claimRead(path string) (<-chan struct{}, bool) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	if wait, ok := r.readFlights[path]; ok {
		return wait, false
	}
	wait := make(chan struct{})
	r.readFlights[path] = wait
	return wait, true
}

func (r *Reader) finishRead(path string) {
	r.readMu.Lock()
	wait := r.readFlights[path]
	delete(r.readFlights, path)
	if wait != nil {
		close(wait)
	}
	r.readMu.Unlock()
}

func (r *Reader) rememberRead(path string, info os.FileInfo, events []Event) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	for index, existing := range r.readOrder {
		if existing == path {
			r.readOrder = append(r.readOrder[:index], r.readOrder[index+1:]...)
			break
		}
	}
	r.readCache[path] = readCacheEntry{
		size: info.Size(), modifiedAt: info.ModTime(), info: info, events: cloneEvents(events),
	}
	r.readOrder = append(r.readOrder, path)
	for len(r.readOrder) > maxReadCacheEntries {
		oldest := r.readOrder[0]
		r.readOrder = r.readOrder[1:]
		delete(r.readCache, oldest)
	}
}

func cloneEvents(events []Event) []Event {
	result := append([]Event(nil), events...)
	for index := range result {
		if result[index].ContextPercent != nil {
			value := *result[index].ContextPercent
			result[index].ContextPercent = &value
		}
	}
	return result
}
