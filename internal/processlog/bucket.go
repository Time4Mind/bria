package processlog

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const logBufferSize = 64 << 10

type bucketWriter struct {
	mu       sync.Mutex
	root     string
	policy   policy
	fallback io.Writer
	now      func() time.Time
	bucketAt time.Time
	path     string
	file     *os.File
	buffer   *bufio.Writer
}

func (writer *bucketWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := writer.rotate(writer.now()); err != nil {
		return writer.fallback.Write(data)
	}
	written, err := writer.buffer.Write(data)
	if writer.policy.level == Critical {
		if flushErr := writer.buffer.Flush(); err == nil {
			err = flushErr
		}
	}
	return written, err
}

func (writer *bucketWriter) rotate(now time.Time) error {
	bucketAt := now.Truncate(writer.policy.bucket)
	if writer.file != nil && bucketAt.Equal(writer.bucketAt) {
		return nil
	}
	if err := writer.closeLocked(); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%s.log", writer.policy.level, bucketAt.Format("20060102T1504"))
	path := filepath.Join(writer.root, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	writer.bucketAt, writer.path, writer.file = bucketAt, path, file
	writer.buffer = bufio.NewWriterSize(file, logBufferSize)
	return nil
}

func (writer *bucketWriter) flushAndRotate(now time.Time) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.file == nil {
		return
	}
	if !now.Truncate(writer.policy.bucket).Equal(writer.bucketAt) {
		_ = writer.closeLocked()
		return
	}
	_ = writer.buffer.Flush()
}

func (writer *bucketWriter) openPath() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.path
}

func (writer *bucketWriter) close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.closeLocked()
}

func (writer *bucketWriter) closeLocked() error {
	if writer.file == nil {
		return nil
	}
	flushErr := writer.buffer.Flush()
	closeErr := writer.file.Close()
	writer.bucketAt, writer.path, writer.file, writer.buffer = time.Time{}, "", nil, nil
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}
