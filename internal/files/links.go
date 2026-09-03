package files

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrInvalidFileLink = errors.New("invalid local file link")

var finalFileLinkPattern = regexp.MustCompile(`(?:\[[^\]\r\n]*\]\((file://[^)\s]+)\)|<(file://[^>\s]+)>)`)

// Link is a deterministic local-file reference extracted from a final answer.
type Link struct {
	Path string
}

// ExtractFinalLinks recognizes only Markdown file-URI destinations and
// autolinks. Bare filesystem paths are deliberately ignored.
func ExtractFinalLinks(final string) ([]Link, error) {
	matches := finalFileLinkPattern.FindAllStringSubmatch(final, -1)
	links := make([]Link, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		raw := match[1]
		if raw == "" {
			raw = match[2]
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "file" || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || !filepath.IsAbs(parsed.Path) || strings.ContainsRune(parsed.Path, 0) {
			return nil, fmt.Errorf("%w", ErrInvalidFileLink)
		}
		path := filepath.Clean(parsed.Path)
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		links = append(links, Link{Path: path})
	}
	return links, nil
}

// OpenFinalFiles extracts, verifies, and opens all unique local links in their
// first-occurrence order. The caller owns every returned descriptor.
func OpenFinalFiles(final string, opener Opener) ([]*VerifiedFile, error) {
	links, err := ExtractFinalLinks(final)
	if err != nil {
		return nil, err
	}
	opened := make([]*VerifiedFile, 0, len(links))
	for _, link := range links {
		file, openErr := opener.OpenRegular(link.Path)
		if openErr != nil {
			for _, previous := range opened {
				_ = previous.Close()
			}
			return nil, fmt.Errorf("open final file: %w", openErr)
		}
		opened = append(opened, file)
	}
	return opened, nil
}
