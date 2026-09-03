package telegram

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProductionHTTPClientRejectsRedirects(t *testing.T) {
	calls := 0
	client := newProductionHTTPClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://attacker.invalid/redirect"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    request,
		}, nil
	}))
	request, err := http.NewRequest(http.MethodPost, "https://api.telegram.org/botsecret/getMe", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("production client redirect response error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || calls != 1 {
		t.Fatalf("redirect status/calls = %d/%d, want 302/1", response.StatusCode, calls)
	}
}

func TestProductionHTTPClientAllowsOnlyOfficialTLSHost(t *testing.T) {
	calls := 0
	client := newProductionHTTPClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    request,
		}, nil
	}))

	for _, target := range []struct {
		url          string
		hostOverride string
	}{
		{url: "http://api.telegram.org/botsecret/getMe"},
		{url: "https://attacker.invalid/botsecret/getMe"},
		{url: "https://api.telegram.org:443/botsecret/getMe"},
		{url: "https://api.telegram.org/botsecret/getMe", hostOverride: "attacker.invalid"},
	} {
		request, err := http.NewRequest(http.MethodPost, target.url, strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("NewRequest(%q) error = %v", target.url, err)
		}
		request.Host = target.hostOverride
		if _, err := client.Do(request); err == nil {
			t.Errorf("production client accepted target %#v", target)
		} else if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), target.url) {
			t.Errorf("production client error exposed request URL: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("rejected destinations reached base transport %d times, want 0", calls)
	}

	request, err := http.NewRequest(http.MethodPost, "https://api.telegram.org/botsecret/getMe", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest(official) error = %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("official request error = %v", err)
	}
	defer response.Body.Close()
	if calls != 1 {
		t.Fatalf("official request calls = %d, want 1", calls)
	}
}

func TestNewProductionHTTPClientBuildsHardenedClient(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:8080")
	t.Setenv("https_proxy", "http://proxy.invalid:8081")

	httpClient := NewProductionHTTPClient()
	client, ok := httpClient.(*productionHTTPClient)
	if !ok || client == nil || client.client == nil || client.client.Transport == nil || client.client.CheckRedirect == nil {
		t.Fatalf("NewProductionHTTPClient() = %#v, want configured transport and redirect policy", httpClient)
	}
	official, ok := client.client.Transport.(officialRoundTripper)
	if !ok {
		t.Fatalf("production transport = %T, want officialRoundTripper", client.client.Transport)
	}
	direct, ok := official.base.(*http.Transport)
	if !ok {
		t.Fatalf("official base transport = %T, want cloned *http.Transport", official.base)
	}
	if direct == http.DefaultTransport {
		t.Fatal("production transport reuses mutable http.DefaultTransport")
	}
	if direct.Proxy != nil {
		t.Fatal("production transport honors HTTPS_PROXY; want direct connection")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
