package runtimehost

import (
	"strings"
	"sync"
)

type localSession struct {
	binding  RuntimeBinding
	executor *LocalExecutor

	mu      sync.Mutex
	wake    *sync.Cond
	pending []Request
	stopped bool
}

func (s *localSession) enqueue(request Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.executor.ctx.Err() != nil {
		return false
	}
	s.pending = append(s.pending, request)
	s.wake.Signal()
	return true
}

func (s *localSession) snapshot() RuntimeBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.binding
}

func (s *localSession) matchesIdentity(binding RuntimeBinding) bool {
	current := s.snapshot()
	return current.NodeID == binding.NodeID && current.SessionID == binding.SessionID &&
		current.Generation == binding.Generation &&
		strings.EqualFold(current.Backend, binding.Backend) && current.Workdir == binding.Workdir
}

func (s *localSession) attach(binding RuntimeBinding) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.binding.NodeID != binding.NodeID || s.binding.SessionID != binding.SessionID ||
		s.binding.Generation != binding.Generation ||
		!strings.EqualFold(s.binding.Backend, binding.Backend) {
		return false
	}
	if s.binding.Workdir != binding.Workdir {
		return false
	}
	if s.binding.TmuxTarget != "" && s.binding.TmuxTarget != binding.TmuxTarget {
		return false
	}
	s.binding = binding
	s.wake.Broadcast()
	return true
}

func (s *localSession) advanceGeneration(expected uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.binding.Generation == expected {
		s.binding.Generation++
	}
}

func (s *localSession) stopAndDrain() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	pending := append([]Request(nil), s.pending...)
	clear(s.pending)
	s.pending = nil
	s.wake.Broadcast()
	return pending
}

func (s *localSession) run() {
	defer s.executor.workers.Done()
	for {
		request, binding, ok := s.next()
		if !ok {
			return
		}
		result, executionErr := s.executor.executeOnce(s.executor.ctx, request, binding)
		if executionErr == nil && result.Delivered && request.Action == ActionClear {
			s.advanceGeneration(request.ExpectedGeneration)
		}
		fingerprint := requestFingerprint(request)
		_ = s.executor.store.Complete(request.OperationID, fingerprint, result, executionErr)
		s.executor.submitMu.Lock()
		delete(s.executor.active, request.OperationID)
		s.executor.submitMu.Unlock()
	}
}

func (s *localSession) next() (Request, RuntimeBinding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for (len(s.pending) == 0 || s.binding.TmuxTarget == "") &&
		!s.stopped && s.executor.ctx.Err() == nil {
		s.wake.Wait()
	}
	if s.executor.ctx.Err() != nil || s.stopped {
		return Request{}, RuntimeBinding{}, false
	}
	request := s.pending[0]
	s.pending[0] = Request{}
	s.pending = s.pending[1:]
	return request, s.binding, true
}
