package providermodels

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"

	"bria/internal/processgroup"
)

const maxOutputBytes = 1 << 20

func (s *Service) collect(ctx context.Context) ([]Model, error) {
	args := append([]string(nil), s.arguments...)
	if !slices.Contains(args, "app-server") {
		args = append(args, "app-server")
	}
	if !slices.Contains(args, "--stdio") {
		args = append(args, "--stdio")
	}
	cmd := exec.CommandContext(ctx, s.executable, args...)
	cmd.Env = append([]string{}, s.environment...)
	if err := processgroup.Configure(cmd); err != nil {
		return nil, err
	}
	cmd.Cancel = func() error { return processgroup.KillTree(cmd) }
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer out.Close()
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = in.Close(); _ = processgroup.KillTree(cmd); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(io.LimitReader(out, maxOutputBytes+1))
	scanner.Buffer(make([]byte, 4096), maxOutputBytes)
	read := func(id int) (json.RawMessage, error) {
		for scanner.Scan() {
			var response struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID == nil || *response.ID != id {
				continue
			}
			if len(response.Error) > 0 && string(response.Error) != "null" {
				return nil, errors.New("Codex model catalog RPC rejected")
			}
			return response.Result, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Codex model catalog response missing or exceeds output bound")
	}
	encoder := json.NewEncoder(in)
	if err := encoder.Encode(map[string]any{"id": 0, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "bria", "version": "1"}}}); err != nil {
		return nil, err
	}
	if _, err := read(0); err != nil {
		return nil, err
	}
	if err := encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	var models []Model
	seenIDs, seenCursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	for page := 1; page <= 10; page++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := encoder.Encode(map[string]any{"id": page, "method": "model/list", "params": params}); err != nil {
			return nil, err
		}
		data, err := read(page)
		if err != nil {
			return nil, err
		}
		entries, next, err := parsePage(data)
		if err != nil {
			return nil, err
		}
		for _, model := range entries {
			if seenIDs[model.ID] {
				continue
			}
			seenIDs[model.ID] = true
			models = append(models, model)
			if len(models) > 256 {
				return nil, errors.New("Codex model catalog exceeds model bound")
			}
		}
		if next == "" {
			if len(models) == 0 {
				return nil, errors.New("Codex model catalog is empty")
			}
			return models, nil
		}
		if seenCursors[next] {
			return nil, errors.New("Codex model catalog repeated cursor")
		}
		seenCursors[next] = true
		cursor = next
	}
	return nil, errors.New("Codex model catalog exceeds page bound")
}

// Fields follow openai/codex app-server-protocol ModelListResponse. Use the
// advertised model slug for turn/start, not an arbitrary catalog entry ID.
func parsePage(data []byte) ([]Model, string, error) {
	var page struct {
		Data []struct {
			ID            string `json:"id"`
			Model         string `json:"model"`
			Name          string `json:"displayName"`
			Hidden        bool   `json:"hidden"`
			Default       bool   `json:"isDefault"`
			DefaultEffort string `json:"defaultReasoningEffort"`
			Efforts       []struct {
				Effort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
		Next string `json:"nextCursor"`
	}
	if json.Unmarshal(data, &page) != nil || page.Data == nil {
		return nil, "", errors.New("invalid Codex model catalog")
	}
	var result []Model
	for _, entry := range page.Data {
		if entry.Hidden {
			continue
		}
		id := entry.Model
		if id == "" {
			id = entry.ID
		}
		if strings.TrimSpace(id) == "" || len(id) > 256 {
			return nil, "", errors.New("invalid Codex model identifier")
		}
		model := Model{ID: id, Name: entry.Name, Default: entry.Default, DefaultEffort: entry.DefaultEffort}
		if model.Name == "" {
			model.Name = id
		}
		for _, effort := range entry.Efforts {
			if effort.Effort != "" && !slices.Contains(model.Efforts, effort.Effort) {
				model.Efforts = append(model.Efforts, effort.Effort)
			}
		}
		result = append(result, model)
	}
	return result, page.Next, nil
}
