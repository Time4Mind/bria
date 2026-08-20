package clusterupdate

import (
	"strconv"
	"strings"
)

type releaseVersion struct {
	major int
	minor int
	patch int
}

// releaseNeedsUpdate treats git-describe builds as commits after their base
// tag. An official release at that same tag is therefore a downgrade, not an
// update. Opaque development versions keep the historical mismatch behavior.
func releaseNeedsUpdate(current, target string) bool {
	if strings.TrimSpace(current) == strings.TrimSpace(target) {
		return false
	}
	targetVersion, ok := parseReleaseVersion(target)
	if !ok {
		return true
	}
	currentVersion, ok := parseReleaseVersion(current)
	if !ok {
		currentVersion, ok = parseGitDescribeVersion(current)
	}
	if !ok {
		return true
	}
	return compareReleaseVersions(currentVersion, targetVersion) < 0
}

func parseReleaseVersion(value string) (releaseVersion, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return releaseVersion{}, false
	}
	values := [3]int{}
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return releaseVersion{}, false
		}
		values[index] = parsed
	}
	return releaseVersion{major: values[0], minor: values[1], patch: values[2]}, true
}

func parseGitDescribeVersion(value string) (releaseVersion, bool) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) == 4 && parts[3] == "dirty" {
		parts = parts[:3]
	}
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "g") || len(parts[2]) < 2 {
		return releaseVersion{}, false
	}
	distance, err := strconv.Atoi(parts[1])
	if err != nil || distance <= 0 {
		return releaseVersion{}, false
	}
	for _, char := range parts[2][1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return releaseVersion{}, false
		}
	}
	return parseReleaseVersion(parts[0])
}

func compareReleaseVersions(left, right releaseVersion) int {
	for _, pair := range [][2]int{
		{left.major, right.major},
		{left.minor, right.minor},
		{left.patch, right.patch},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}
