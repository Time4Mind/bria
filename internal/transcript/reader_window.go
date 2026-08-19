package transcript

func parseRecentEvents(
	backend Backend,
	lines [][]byte,
	maxBodyBytes int,
	maxEvents int,
) ([]Event, int) {
	if len(lines) == 0 {
		return nil, 0
	}
	count := min(initialParseLines, len(lines))
	for {
		suffix := lines[len(lines)-count:]
		var events []Event
		switch backend {
		case BackendClaude:
			events = parseClaude(suffix, maxBodyBytes)
		case BackendCodex:
			events = parseCodex(suffix, maxBodyBytes)
		}
		hasContext := false
		for index := len(events) - 1; index >= 0; index-- {
			if events[index].ContextPercent != nil {
				hasContext = true
				break
			}
		}
		if count == len(lines) || (visibleEventCount(events) >= maxEvents && hasContext) {
			return trimRecentVisibleEvents(events, maxEvents), count
		}
		count = min(len(lines), count*2)
	}
}

func visibleEventCount(events []Event) int {
	count := 0
	for _, event := range events {
		if event.Kind != EventTurnComplete {
			count++
		}
	}
	return count
}

func trimRecentVisibleEvents(events []Event, limit int) []Event {
	visible := 0
	start := 0
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == EventTurnComplete {
			continue
		}
		visible++
		if visible > limit {
			start = index + 1
			break
		}
	}
	return events[start:]
}
