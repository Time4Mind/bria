// Package providerquota collects bounded read-only provider usage snapshots.
package providerquota

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"bria/internal/claudequota"
	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/processenv"
	"bria/internal/processgroup"
	"bria/internal/telegramstatus"
)

const maxOutputBytes = 1 << 20

type Command struct {
	Provider    domain.Provider
	Executable  string
	Arguments   []string
	Environment []string
}

type Service struct {
	nodeID   domain.ComputerID
	commands []Command
	interval time.Duration
	claude   *claudequota.Collector

	mu        sync.RWMutex
	snapshots map[domain.Provider]telegramstatus.Snapshot
}

func New(nodeID domain.ComputerID, commands []Command, interval time.Duration) (*Service, error) {
	if nodeID == "" || interval < time.Minute {
		return nil, errors.New("quota node and interval are required")
	}
	copyCommands := make([]Command, 0, len(commands))
	for _, command := range commands {
		if (command.Provider != domain.ProviderCodex && command.Provider != domain.ProviderClaude) ||
			command.Executable == "" || command.Environment == nil {
			continue
		}
		command.Arguments = append([]string(nil), command.Arguments...)
		command.Environment = append([]string(nil), command.Environment...)
		copyCommands = append(copyCommands, command)
	}
	service := &Service{nodeID: nodeID, commands: copyCommands, interval: interval,
		snapshots: make(map[domain.Provider]telegramstatus.Snapshot)}
	for _, command := range copyCommands {
		service.snapshots[command.Provider] = telegramstatus.Snapshot{ComputerID: nodeID, Provider: command.Provider}
		if command.Provider == domain.ProviderClaude {
			service.claude = claudequota.New(nodeID, claudequota.Spec{
				Executable: command.Executable, Arguments: command.Arguments, Environment: command.Environment,
			})
		}
	}
	return service, nil
}

func FromConfig(nodeID domain.ComputerID, configuration config.Config, environment []string, interval time.Duration) (*Service, error) {
	safeEnvironment, err := processenv.Build(environment, processenv.Options{TelegramTokenEnv: configuration.TelegramToken.EnvVar})
	if err != nil {
		return nil, err
	}
	commands := make([]Command, 0, 2)
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		command, enabled := configuration.EnabledCommand(provider)
		if enabled {
			commands = append(commands, Command{Provider: provider, Executable: command.Exec, Arguments: command.Argv, Environment: safeEnvironment})
		}
	}
	return New(nodeID, commands, interval)
}

func (service *Service) Snapshots(ctx context.Context) ([]telegramstatus.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	result := make([]telegramstatus.Snapshot, 0, len(service.snapshots))
	for _, snapshot := range service.snapshots {
		result = append(result, clone(snapshot))
	}
	return result, nil
}

func (service *Service) Run(ctx context.Context) error {
	defer service.closeClaude()
	service.collect(ctx)
	ticker := time.NewTicker(service.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			service.collect(ctx)
		}
	}
}

// Refresh performs an on-demand bounded poll for the Status/Nodes buttons.
func (service *Service) Refresh(ctx context.Context) error {
	if service == nil {
		return errors.New("quota service is unavailable")
	}
	service.collect(ctx)
	return ctx.Err()
}

func (service *Service) collect(ctx context.Context) {
	for _, command := range service.commands {
		timeout := 10 * time.Second
		if command.Provider == domain.ProviderClaude {
			timeout = 30 * time.Second
		}
		collectCtx, cancel := context.WithTimeout(ctx, timeout)
		var snapshot telegramstatus.Snapshot
		var err error
		switch command.Provider {
		case domain.ProviderCodex:
			snapshot, err = collectCodex(collectCtx, service.nodeID, command)
		case domain.ProviderClaude:
			snapshot, err = service.claude.Collect(collectCtx)
		}
		cancel()
		if err != nil {
			continue
		}
		service.mu.Lock()
		service.snapshots[command.Provider] = snapshot
		service.mu.Unlock()
	}
}

func (service *Service) closeClaude() {
	if service.claude == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	service.claude.Close(ctx)
}

func collectCodex(ctx context.Context, nodeID domain.ComputerID, specification Command) (telegramstatus.Snapshot, error) {
	arguments := append([]string(nil), specification.Arguments...)
	if !slices.Contains(arguments, "app-server") {
		arguments = append(arguments, "app-server")
	}
	if !slices.Contains(arguments, "--stdio") {
		arguments = append(arguments, "--stdio")
	}
	command := exec.CommandContext(ctx, specification.Executable, arguments...)
	command.Env = append([]string(nil), specification.Environment...)
	if err := processgroup.Configure(command); err != nil {
		return telegramstatus.Snapshot{}, err
	}
	command.Cancel = func() error { return processgroup.KillTree(command) }
	stdin, err := command.StdinPipe()
	if err != nil {
		return telegramstatus.Snapshot{}, err
	}
	defer stdin.Close()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return telegramstatus.Snapshot{}, err
	}
	defer stdout.Close()
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return telegramstatus.Snapshot{}, err
	}
	defer func() {
		_ = stdin.Close()
		_ = processgroup.KillTree(command)
		_ = command.Wait()
	}()
	scanner := bufio.NewScanner(io.LimitReader(stdout, maxOutputBytes+1))
	scanner.Buffer(make([]byte, 4096), maxOutputBytes)
	readResponse := func(id int) ([]byte, error) {
		for scanner.Scan() {
			var response struct {
				ID    *int            `json:"id"`
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID == nil || *response.ID != id {
				continue
			}
			if len(response.Error) != 0 && string(response.Error) != "null" {
				return nil, errors.New("Codex quota RPC rejected")
			}
			return append([]byte(nil), scanner.Bytes()...), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if scanner.Err() != nil {
			return nil, scanner.Err()
		}
		return nil, errors.New("Codex quota RPC ended without response")
	}
	if _, err := io.WriteString(stdin, `{"method":"initialize","id":0,"params":{"clientInfo":{"name":"bria","title":"Bria","version":"1"}}}`+"\n"); err != nil {
		return telegramstatus.Snapshot{}, err
	}
	if _, err := readResponse(0); err != nil {
		return telegramstatus.Snapshot{}, err
	}
	if _, err := io.WriteString(stdin, `{"method":"initialized","params":{}}`+"\n"+`{"method":"account/rateLimits/read","id":1,"params":{}}`+"\n"); err != nil {
		return telegramstatus.Snapshot{}, err
	}
	response, err := readResponse(1)
	if err != nil {
		return telegramstatus.Snapshot{}, err
	}
	return parseCodex(response, nodeID, time.Now().UTC())
}

func parseCodex(data []byte, nodeID domain.ComputerID, collected time.Time) (telegramstatus.Snapshot, error) {
	snapshot := telegramstatus.Snapshot{ComputerID: nodeID, Provider: domain.ProviderCodex, CollectedAt: collected}
	found := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxOutputBytes)
	for scanner.Scan() {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID != 1 {
			continue
		}
		var result map[string]any
		if json.Unmarshal(response.Result, &result) != nil {
			continue
		}
		if limits, ok := result["rateLimits"].(map[string]any); ok {
			found = mergeLimits(&snapshot, limits) || found
		}
		if groups, ok := result["rateLimitsByLimitId"].(map[string]any); ok {
			for key, value := range groups {
				limits, ok := value.(map[string]any)
				if ok && isCodexLimitBucket(key, limits) {
					found = mergeLimits(&snapshot, limits) || found
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return telegramstatus.Snapshot{}, err
	}
	if !found {
		return telegramstatus.Snapshot{}, errors.New("Codex returned no rate limits")
	}
	return snapshot, nil
}

func isCodexLimitBucket(key string, limits map[string]any) bool {
	for _, value := range []any{key, limits["limitId"], limits["limitName"]} {
		text, ok := value.(string)
		if ok && strings.EqualFold(strings.TrimSpace(text), "codex") {
			return true
		}
	}
	return false
}

func mergeLimits(snapshot *telegramstatus.Snapshot, limits map[string]any) bool {
	found := false
	for _, key := range []string{"primary", "secondary"} {
		raw, ok := limits[key].(map[string]any)
		used, usedOK := raw["usedPercent"].(float64)
		if !ok || !usedOK {
			continue
		}
		window := &telegramstatus.Window{UsedPercent: min(100, max(0, int(used+0.5)))}
		if reset, ok := raw["resetsAt"].(float64); ok && reset > 0 {
			window.ResetsAt = time.Unix(int64(reset), 0).UTC()
		}
		duration, _ := raw["windowDurationMins"].(float64)
		if duration < 24*60 {
			snapshot.FiveHour = window
		} else {
			snapshot.Weekly = window
		}
		found = true
	}
	return found
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	allowed := buffer.limit - buffer.Len()
	if allowed <= 0 {
		buffer.exceeded = true
		return len(data), nil
	}
	if len(data) > allowed {
		buffer.exceeded = true
		_, _ = buffer.Buffer.Write(data[:allowed])
		return len(data), nil
	}
	return buffer.Buffer.Write(data)
}

func clone(snapshot telegramstatus.Snapshot) telegramstatus.Snapshot {
	if snapshot.FiveHour != nil {
		window := *snapshot.FiveHour
		snapshot.FiveHour = &window
	}
	if snapshot.Weekly != nil {
		window := *snapshot.Weekly
		snapshot.Weekly = &window
	}
	if snapshot.TodayRemaining != nil {
		remaining := *snapshot.TodayRemaining
		snapshot.TodayRemaining = &remaining
	}
	return snapshot
}
