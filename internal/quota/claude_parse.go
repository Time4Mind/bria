package quota

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

var (
	percentUsed = regexp.MustCompile(`(?i)(\d{1,3})\s*%\s*used`)
	clockValue  = regexp.MustCompile(`(?i)(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b`)
	monthDay    = regexp.MustCompile(`(?i)\b(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\s+(\d{1,2})\b`)
)

func ParseClaudeUsage(text string, nodeID domain.NodeID, now time.Time) (domain.QuotaSnapshot, bool) {
	snapshot := domain.QuotaSnapshot{NodeID: nodeID, Backend: "claude", CollectedAt: now.UTC()}
	section := ""
	found := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.Contains(line, "Current session"):
			section = "session"
		case strings.Contains(line, "Current week") && strings.Contains(strings.ToLower(line), "all models"):
			section = "week"
		default:
			window := snapshotWindow(&snapshot, section)
			if window == nil {
				continue
			}
			if match := percentUsed.FindStringSubmatch(line); match != nil {
				value, _ := strconv.Atoi(match[1])
				window.UsedPercent = min(100, max(0, value))
				found = true
			} else if strings.HasPrefix(strings.ToLower(line), "resets") {
				window.ResetsAt = resetTime(line, now)
			}
		}
	}
	if !found {
		return domain.QuotaSnapshot{}, false
	}
	return snapshot, true
}

func resetTime(value string, now time.Time) time.Time {
	clock := clockValue.FindStringSubmatch(value)
	date := monthDay.FindStringSubmatch(value)
	if date == nil {
		return nextClock(clock, now)
	}
	month, ok := parseMonth(date[1])
	if !ok || clock == nil {
		return time.Time{}
	}
	day, _ := strconv.Atoi(date[2])
	hour, minute := parseClock(clock)
	result := time.Date(now.Year(), month, day, hour, minute, 0, 0, now.Location())
	if !result.After(now) {
		result = time.Date(now.Year()+1, month, day, hour, minute, 0, 0, now.Location())
	}
	return result.UTC()
}

func snapshotWindow(snapshot *domain.QuotaSnapshot, section string) *domain.QuotaWindow {
	switch section {
	case "session":
		if snapshot.FiveHour == nil {
			snapshot.FiveHour = &domain.QuotaWindow{}
		}
		return snapshot.FiveHour
	case "week":
		if snapshot.Weekly == nil {
			snapshot.Weekly = &domain.QuotaWindow{}
		}
		return snapshot.Weekly
	default:
		return nil
	}
}

func nextClock(match []string, now time.Time) time.Time {
	if match == nil {
		return time.Time{}
	}
	hour, minute := parseClock(match)
	value := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !value.After(now) {
		value = value.Add(24 * time.Hour)
	}
	return value.UTC()
}

func parseClock(match []string) (int, int) {
	hour, _ := strconv.Atoi(match[1])
	minute, _ := strconv.Atoi(match[2])
	if strings.EqualFold(match[3], "pm") && hour != 12 {
		hour += 12
	} else if strings.EqualFold(match[3], "am") && hour == 12 {
		hour = 0
	}
	return hour, minute
}

func parseMonth(value string) (time.Month, bool) {
	for month, name := range []string{
		"Jan", "Feb", "Mar", "Apr", "May", "Jun",
		"Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
	} {
		if strings.EqualFold(value[:min(3, len(value))], name) {
			return time.Month(month + 1), true
		}
	}
	return 0, false
}
