package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

const kimiCodingUsagePath = "/coding/v1/usages"

type kimiUsageSource interface {
	Collect(context.Context, domain.NodeID) (domain.QuotaSnapshot, bool, error)
}

type kimiUsageClient struct {
	httpClient  *http.Client
	credentials func() (kimiUsageCredentials, bool, error)
}

type kimiUsageCredentials struct {
	Endpoint string
	APIKey   string
}

type kimiUsageDetail struct {
	Limit     json.RawMessage `json:"limit"`
	Used      json.RawMessage `json:"used"`
	Remaining json.RawMessage `json:"remaining"`
	ResetTime string          `json:"resetTime"`
}

type kimiUsageResponse struct {
	User struct {
		UserID string `json:"userId"`
	} `json:"user"`
	Usage  kimiUsageDetail `json:"usage"`
	Limits []struct {
		Window struct {
			Duration json.RawMessage `json:"duration"`
			TimeUnit string          `json:"timeUnit"`
		} `json:"window"`
		Detail kimiUsageDetail `json:"detail"`
	} `json:"limits"`
}

func newKimiUsageSource() kimiUsageSource {
	return &kimiUsageClient{
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		credentials: kimiCredentialsFromClaude,
	}
}

func (c *kimiUsageClient) Collect(
	ctx context.Context,
	nodeID domain.NodeID,
) (domain.QuotaSnapshot, bool, error) {
	credentials, configured, err := c.credentials()
	if err != nil || !configured {
		return domain.QuotaSnapshot{}, configured, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, credentials.Endpoint, nil)
	if err != nil {
		return domain.QuotaSnapshot{}, true, err
	}
	request.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	request.Header.Set("x-api-key", credentials.APIKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return domain.QuotaSnapshot{}, true, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return domain.QuotaSnapshot{}, true, err
	}
	if response.StatusCode != http.StatusOK {
		return domain.QuotaSnapshot{}, true, fmt.Errorf("Kimi usage returned HTTP %d", response.StatusCode)
	}
	snapshot, err := parseKimiUsage(body, nodeID, time.Now())
	return snapshot, true, err
}

func kimiCredentialsFromClaude() (kimiUsageCredentials, bool, error) {
	environment := make(map[string]string)
	configDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return kimiUsageCredentials{}, false, err
		}
		configDir = filepath.Join(home, ".claude")
	}
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err == nil {
		var settings struct {
			Environment map[string]string `json:"env"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return kimiUsageCredentials{}, false, fmt.Errorf("parse Claude settings: %w", err)
		}
		for key, value := range settings.Environment {
			environment[key] = value
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return kimiUsageCredentials{}, false, err
	}
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			environment[key] = value
		}
	}
	endpoint, isKimi := kimiUsageEndpoint(environment["ANTHROPIC_BASE_URL"])
	if !isKimi {
		return kimiUsageCredentials{}, false, nil
	}
	apiKey := strings.TrimSpace(environment["ANTHROPIC_API_KEY"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(environment["ANTHROPIC_AUTH_TOKEN"])
	}
	if apiKey == "" {
		return kimiUsageCredentials{}, true, errors.New("Kimi API key is missing from Claude settings")
	}
	return kimiUsageCredentials{Endpoint: endpoint, APIKey: apiKey}, true, nil
}

func kimiUsageEndpoint(baseURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") ||
		!strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "api.kimi.com") {
		return "", false
	}
	parsed.Path = kimiCodingUsagePath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

func parseKimiUsage(
	data []byte,
	nodeID domain.NodeID,
	collectedAt time.Time,
) (domain.QuotaSnapshot, error) {
	var response kimiUsageResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&response); err != nil {
		return domain.QuotaSnapshot{}, fmt.Errorf("parse Kimi usage: %w", err)
	}
	snapshot := domain.QuotaSnapshot{
		NodeID: nodeID, Backend: "claude", AccountID: response.User.UserID,
		CollectedAt: collectedAt.UTC(),
	}
	if window, ok := kimiQuotaWindow(response.Usage); ok {
		snapshot.Weekly = &window
	}
	for _, limit := range response.Limits {
		if !isFiveHourKimiWindow(limit.Window.Duration, limit.Window.TimeUnit) {
			continue
		}
		if window, ok := kimiQuotaWindow(limit.Detail); ok {
			snapshot.FiveHour = &window
			break
		}
	}
	if snapshot.FiveHour == nil && snapshot.Weekly == nil {
		return domain.QuotaSnapshot{}, errors.New("Kimi returned no quota windows")
	}
	return snapshot, snapshot.Validate()
}

func kimiQuotaWindow(detail kimiUsageDetail) (domain.QuotaWindow, bool) {
	limit, limitOK := rawFloat(detail.Limit)
	if !limitOK || limit <= 0 {
		return domain.QuotaWindow{}, false
	}
	used, usedOK := rawFloat(detail.Used)
	if !usedOK {
		remaining, remainingOK := rawFloat(detail.Remaining)
		if !remainingOK {
			return domain.QuotaWindow{}, false
		}
		used = limit - remaining
	}
	window := domain.QuotaWindow{
		UsedPercent: min(100, max(0, int(math.Round(used/limit*100)))),
	}
	if reset, err := time.Parse(time.RFC3339Nano, detail.ResetTime); err == nil {
		window.ResetsAt = reset.UTC()
	}
	return window, true
}

func isFiveHourKimiWindow(duration json.RawMessage, unit string) bool {
	value, ok := rawFloat(duration)
	if !ok {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "TIME_UNIT_MINUTE", "MINUTE", "MINUTES":
		return value == 300
	case "TIME_UNIT_HOUR", "HOUR", "HOURS":
		return value == 5
	case "TIME_UNIT_SECOND", "SECOND", "SECONDS":
		return value == 5*60*60
	default:
		return false
	}
}

func rawFloat(value json.RawMessage) (float64, bool) {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return 0, false
	}
	if unquoted, err := strconv.Unquote(trimmed); err == nil {
		trimmed = unquoted
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}
