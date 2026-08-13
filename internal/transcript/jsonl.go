package transcript

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
)

const maxMetadataReadBytes = 256 << 10

func readRecentJSONLLines(path string, maxBytes int64, maxLineBytes int) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat transcript: %w", err)
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek transcript: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	parts := bytes.Split(raw, []byte{'\n'})
	if start > 0 && len(parts) > 0 {
		parts = parts[1:]
	}
	return boundedLines(parts, maxLineBytes), nil
}

func readLeadingJSONLLines(path string, count, maxLineBytes int) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	limited := io.LimitReader(file, min(int64(maxMetadataReadBytes), int64(count)*int64(maxLineBytes+1)))
	reader := bufio.NewReaderSize(limited, min(maxLineBytes, 64<<10))
	lines := make([][]byte, 0, count)
	for attempts := 0; attempts < count; attempts++ {
		line, oversized, readErr := readBoundedLine(reader, maxLineBytes)
		if !oversized {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				lines = append(lines, line)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, readErr
		}
	}
	return lines, nil
}

func readBoundedLine(reader *bufio.Reader, maxLineBytes int) ([]byte, bool, error) {
	line := make([]byte, 0, min(maxLineBytes, 64<<10))
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if !oversized && len(line)+len(fragment) <= maxLineBytes {
			line = append(line, fragment...)
		} else {
			oversized = true
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, oversized, err
	}
}

func boundedLines(parts [][]byte, maxLineBytes int) [][]byte {
	lines := make([][]byte, 0, len(parts))
	for _, line := range parts {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || len(line) > maxLineBytes {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}
