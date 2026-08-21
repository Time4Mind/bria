// Package processlog owns bounded local operational logs for a Bria node.
package processlog

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Level string

const (
	Detail   Level = "detail"
	Service  Level = "service"
	Critical Level = "critical"
)

type policy struct {
	level     Level
	retention time.Duration
	bucket    time.Duration
}

type Identity struct {
	Version string
	Commit  string
}

var policies = []policy{
	{level: Detail, retention: 6 * time.Hour, bucket: 30 * time.Minute},
	{level: Service, retention: 24 * time.Hour, bucket: time.Hour},
	{level: Critical, retention: 72 * time.Hour, bucket: 6 * time.Hour},
}

type Manager struct {
	root      string
	fallback  io.Writer
	streams   map[Level]*bucketWriter
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	previous  writerSet
	logOutput io.Writer
	logFlags  int
	logPrefix string
	rawPath   string
}

type writerSet struct {
	detail   io.Writer
	service  io.Writer
	critical io.Writer
	identity Identity
}

var routing = struct {
	sync.RWMutex
	writers writerSet
}{writers: writerSet{
	detail: os.Stderr, service: os.Stderr, critical: os.Stderr,
	identity: Identity{Version: "unknown", Commit: "unknown"},
}}

// Start activates bucketed logging and adopts the supervisor's current raw
// node.log as a critical fallback bucket. Application logs are then routed by
// explicit level; no line-oriented file rewrite is required for retention.
func Start(root string, identity Identity) (*Manager, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return nil, errors.New("process log root must be absolute")
	}
	if err := identity.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create process log root: %w", err)
	}
	fallback := os.Stderr
	rawPath, err := adoptRawLog(root, time.Now())
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		root: root, fallback: fallback, streams: make(map[Level]*bucketWriter, len(policies)),
		stop: make(chan struct{}), done: make(chan struct{}),
		logOutput: log.Writer(), logFlags: log.Flags(), logPrefix: log.Prefix(), rawPath: rawPath,
	}
	for _, item := range policies {
		manager.streams[item.level] = &bucketWriter{
			root: root, policy: item, fallback: fallback, now: time.Now,
		}
	}
	routing.Lock()
	manager.previous = routing.writers
	routing.writers = writerSet{
		detail: manager.streams[Detail], service: manager.streams[Service],
		critical: manager.streams[Critical], identity: identity,
	}
	routing.Unlock()
	log.SetOutput(Writer(Service))
	go manager.maintain()
	if err := cleanupExpired(root, time.Now(), manager.openPaths()); err != nil {
		manager.Close()
		return nil, err
	}
	return manager, nil
}

func (m *Manager) maintain() {
	flushTicker := time.NewTicker(time.Second)
	cleanupTicker := time.NewTicker(10 * time.Minute)
	defer func() {
		flushTicker.Stop()
		cleanupTicker.Stop()
		close(m.done)
	}()
	for {
		select {
		case <-flushTicker.C:
			m.flush(time.Now())
		case <-cleanupTicker.C:
			now := time.Now()
			m.flush(now)
			_ = cleanupExpired(m.root, now, m.openPaths())
		case <-m.stop:
			return
		}
	}
}

func (m *Manager) flush(now time.Time) {
	for _, stream := range m.streams {
		stream.flushAndRotate(now)
	}
}

func (m *Manager) openPaths() map[string]bool {
	result := make(map[string]bool, len(m.streams))
	for _, stream := range m.streams {
		if path := stream.openPath(); path != "" {
			result[path] = true
		}
	}
	if m.rawPath != "" {
		result[m.rawPath] = true
	}
	return result
}

func (m *Manager) Close() error {
	var result error
	m.closeOnce.Do(func() {
		close(m.stop)
		<-m.done
		for _, stream := range m.streams {
			result = errors.Join(result, stream.close())
		}
		routing.Lock()
		routing.writers = m.previous
		routing.Unlock()
		log.SetOutput(m.logOutput)
		log.SetFlags(m.logFlags)
		log.SetPrefix(m.logPrefix)
	})
	return result
}

func Writer(level Level) io.Writer { return routedWriter{level: level} }

func Detailf(format string, args ...any)   { writef(Detail, format, args...) }
func Servicef(format string, args ...any)  { writef(Service, format, args...) }
func Criticalf(format string, args ...any) { writef(Critical, format, args...) }

func writef(level Level, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	_, _ = writeStructured(level, classifyFailure(level, message), message)
}

type routedWriter struct{ level Level }

func (writer routedWriter) Write(data []byte) (int, error) {
	routing.RLock()
	target := routing.writers.service
	switch writer.level {
	case Detail:
		target = routing.writers.detail
	case Critical:
		target = routing.writers.critical
	}
	routing.RUnlock()
	return target.Write(data)
}

func writeStructured(level Level, class FailureClass, message string) (int, error) {
	routing.RLock()
	target := routing.writers.service
	identity := routing.writers.identity
	switch level {
	case Detail:
		target = routing.writers.detail
	case Critical:
		target = routing.writers.critical
	}
	routing.RUnlock()
	record := formatStructuredRecord(
		time.Now().UTC(), os.Getpid(), identity, level,
		normalizeFailureClass(class), message,
	)
	return target.Write(record)
}
