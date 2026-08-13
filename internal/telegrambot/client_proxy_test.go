package telegrambot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientUsesExplicitHTTPProxyAndDoesNotInheritEnvironment(t *testing.T) {
	proxyHits := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proxyHits++
		if request.URL.Host != "telegram.invalid" ||
			request.URL.Path != "/bot"+testToken+"/getMe" {
			t.Errorf("proxied URL=%s", request.URL.String())
		}
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{
				"id": 1, "is_bot": true, "username": "proxy_test_bot",
			},
		})
	}))
	defer proxy.Close()
	client, err := NewClient(ClientConfig{
		Token: testToken, BaseURL: "http://telegram.invalid", AllowInsecureHTTP: true,
		ProxyURL: proxy.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetMe(context.Background()); err != nil {
		t.Fatalf("GetMe through proxy: %v", err)
	}
	if proxyHits != 1 {
		t.Fatalf("proxy hits=%d", proxyHits)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{
				"id": 1, "is_bot": true, "username": "direct_test_bot",
			},
		})
	}))
	defer origin.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	direct, err := NewClient(ClientConfig{
		Token: testToken, BaseURL: origin.URL, AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := direct.GetMe(context.Background()); err != nil {
		t.Fatalf("typed direct transport inherited environment proxy: %v", err)
	}
}

func TestClientRejectsMalformedProxyAndCustomTransportCombination(t *testing.T) {
	if _, err := NewClient(ClientConfig{
		Token: testToken, ProxyURL: "file:///tmp/proxy",
	}); err == nil {
		t.Fatal("malformed proxy accepted")
	}
	if _, err := NewClient(ClientConfig{
		Token: testToken, ProxyURL: "http://127.0.0.1:1081", HTTPClient: http.DefaultClient,
	}); err == nil {
		t.Fatal("ambiguous proxy and custom HTTP client accepted")
	}
}

func TestConfiguredProxyFailureDoesNotFallBackToDirectTelegram(t *testing.T) {
	originHits := 0
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		originHits++
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{
				"id": 1, "is_bot": true, "username": "unexpected_direct_bot",
			},
		})
	}))
	defer origin.Close()
	failedProxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	proxyURL := failedProxy.URL
	failedProxy.Close()
	client, err := NewClient(ClientConfig{
		Token: testToken, BaseURL: origin.URL, AllowInsecureHTTP: true, ProxyURL: proxyURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetMe(context.Background()); err == nil {
		t.Fatal("request unexpectedly bypassed unavailable configured proxy")
	}
	if originHits != 0 {
		t.Fatalf("direct Telegram fallback requests=%d", originHits)
	}
}
