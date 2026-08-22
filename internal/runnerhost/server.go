package runnerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type Server struct {
	runner   runtimehost.JSONRPCCommandRunner
	http     *http.Server
	auth     *authStore
	bindings *providerbinding.Store
}

func NewServer(runner runtimehost.JSONRPCCommandRunner) (*Server, error) {
	return NewServerWithBindings(runner, nil)
}

func NewServerWithBindings(runner runtimehost.JSONRPCCommandRunner, bindings *providerbinding.Store) (*Server, error) {
	if runner == nil {
		return nil, errors.New("runner is required")
	}
	server := &Server{runner: runner, auth: newAuthStore(), bindings: bindings}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/inspect", server.inspect)
	mux.HandleFunc("/v1/look-path", server.lookPath)
	mux.HandleFunc("/v1/run", server.run)
	mux.HandleFunc("/v1/auth/start", server.authStart)
	mux.HandleFunc("/v1/auth/submit", server.authSubmit)
	mux.HandleFunc("/v1/auth/status", server.authStatus)
	mux.HandleFunc("/v1/auth/cancel", server.authCancel)
	mux.HandleFunc("/v1/provider-binding/lookup", server.bindingLookup)
	mux.HandleFunc("/v1/provider-binding/snapshot", server.bindingSnapshot)
	mux.HandleFunc("/v1/provider-binding/sweep", server.bindingSweep)
	mux.HandleFunc("/v1/provider-binding/delete", server.bindingDelete)
	server.http = &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	return server, nil
}

func (s *Server) bindingLookup(writer http.ResponseWriter, request *http.Request) {
	var input bindingLookupRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	if s.bindings == nil {
		http.Error(writer, "provider bindings unavailable", http.StatusServiceUnavailable)
		return
	}
	var record providerbinding.Record
	var found bool
	var err error
	if input.Workdir == "" {
		record, found, err = s.bindings.LookupRef(input.Ref)
	} else {
		record, found, err = s.bindings.Lookup(input.Ref, input.Workdir)
	}
	response := bindingLookupResponse{Record: record, Found: found}
	if err != nil {
		response.Error = err.Error()
	}
	writeJSON(writer, response)
}

func (s *Server) bindingSnapshot(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.bindings == nil {
		http.Error(writer, "provider bindings unavailable", http.StatusServiceUnavailable)
		return
	}
	records, err := s.bindings.Snapshot()
	response := bindingSnapshotResponse{Records: records}
	if err != nil {
		response.Error = err.Error()
	}
	writeJSON(writer, response)
}

func (s *Server) bindingSweep(writer http.ResponseWriter, request *http.Request) {
	var input bindingSweepRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	s.bindingMutation(writer, func() error { return s.bindings.Sweep(input.Input) })
}

func (s *Server) bindingDelete(writer http.ResponseWriter, request *http.Request) {
	var input bindingDeleteRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	s.bindingMutation(writer, func() error {
		return s.bindings.DeleteIfGeneration(input.Ref, input.Generation)
	})
}

func (s *Server) bindingMutation(writer http.ResponseWriter, mutate func() error) {
	if s.bindings == nil {
		http.Error(writer, "provider bindings unavailable", http.StatusServiceUnavailable)
		return
	}
	response := bindingMutationResponse{}
	if err := mutate(); err != nil {
		response.Error = err.Error()
	}
	writeJSON(writer, response)
}

func (s *Server) Serve(socket string) error {
	if !filepath.IsAbs(socket) {
		return errors.New("runner socket must be absolute")
	}
	if err := removeStaleSocket(socket); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen on runner socket: %w", err)
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		_ = listener.Close()
		_ = os.Remove(socket)
		return fmt.Errorf("secure runner socket: %w", err)
	}
	defer os.Remove(socket)
	if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return errors.Join(s.auth.close(), s.http.Shutdown(ctx))
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runner socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("refusing to replace a non-socket runner path")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale runner socket: %w", err)
	}
	return nil
}

func (s *Server) inspect(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(writer, localInspect())
}

func (s *Server) lookPath(writer http.ResponseWriter, request *http.Request) {
	var input pathRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 4096 {
		http.Error(writer, "invalid executable", http.StatusBadRequest)
		return
	}
	path, err := s.runner.LookPath(input.Name)
	response := pathResponse{Path: path}
	if err != nil {
		response.Error = err.Error()
	}
	writeJSON(writer, response)
}

func (s *Server) run(writer http.ResponseWriter, request *http.Request) {
	var input commandRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 4096 ||
		input.TimeoutMS < 1 || input.TimeoutMS > int64((10*time.Minute)/time.Millisecond) {
		http.Error(writer, "invalid command request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), time.Duration(input.TimeoutMS)*time.Millisecond)
	defer cancel()
	var result runtimehost.CommandResult
	var err error
	switch input.Kind {
	case "run":
		result, err = s.runner.Run(ctx, input.Name, input.Args...)
	case "input":
		result, err = s.runner.RunInput(ctx, input.Input, input.Name, input.Args...)
	case "jsonrpc":
		result, err = s.runner.RunJSONRPC(
			ctx, input.Initialize, input.Input, input.ExpectedID, input.Name, input.Args...,
		)
	default:
		http.Error(writer, "unknown command kind", http.StatusBadRequest)
		return
	}
	if len(result.Stdout)+len(result.Stderr) > maxResultBytes {
		result = runtimehost.CommandResult{}
		err = errors.New("runner output exceeded its limit")
	}
	response := commandResponse{Result: result}
	if err != nil {
		response.Error = err.Error()
	}
	writeJSON(writer, response)
}

func decodeRequest(writer http.ResponseWriter, request *http.Request, target any) bool {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(writer, "invalid request trailer", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}
