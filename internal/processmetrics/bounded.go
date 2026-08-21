package processmetrics

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

const directoryBatchSize = 128

func walkNumericDirectory(
	ctx context.Context,
	path string,
	limit int,
	visit func(string) error,
) (int, bool, error) {
	directory, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer directory.Close()
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return count, false, err
		}
		names, readErr := directory.Readdirnames(directoryBatchSize)
		for _, name := range names {
			if _, parseErr := strconv.ParseUint(name, 10, 64); parseErr != nil {
				continue
			}
			if count >= limit {
				return count, true, nil
			}
			count++
			if visit != nil {
				if visitErr := visit(name); visitErr != nil {
					return count, false, visitErr
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return count, false, nil
		}
		if readErr != nil {
			return count, false, readErr
		}
	}
}

func countOpenFDs(ctx context.Context, path string, limit int) (int, bool, error) {
	// Opening the directory adds the enumerator's own descriptor to the view.
	// Read at most two entries beyond the public limit so that the subtraction
	// cannot hide a genuinely capped process descriptor.
	count, capped, err := walkNumericDirectory(ctx, path, limit+2, nil)
	if err != nil {
		return 0, false, err
	}
	if count > 0 {
		count--
	}
	if count > limit {
		count = limit
		capped = true
	}
	return count, capped, nil
}

func scanChildPIDs(
	ctx context.Context,
	reader io.Reader,
	children map[string]struct{},
	limit int,
) (bool, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Split(bufio.ScanWords)
	scanner.Buffer(make([]byte, 64), 64)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		child := scanner.Text()
		if _, err := strconv.ParseUint(child, 10, 64); err != nil {
			continue
		}
		if _, exists := children[child]; exists {
			continue
		}
		if len(children) >= limit {
			return true, nil
		}
		children[child] = struct{}{}
	}
	return false, scanner.Err()
}

func parseRSSKibibytes(value string) (int64, bool) {
	kibibytes, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || kibibytes < 0 || kibibytes > math.MaxInt64/1024 {
		return 0, false
	}
	return kibibytes * 1024, true
}

func parseRSSPages(value string, pageSize int64) (int64, bool) {
	fields := strings.Fields(value)
	if len(fields) < 2 || pageSize <= 0 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || pages < 0 || pages > math.MaxInt64/pageSize {
		return 0, false
	}
	return pages * pageSize, true
}
