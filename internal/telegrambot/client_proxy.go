package telegrambot

import (
	"errors"
	"net/http"
	"net/url"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func configuredHTTPClient(custom HTTPDoer, rawProxyURL string) (HTTPDoer, error) {
	if custom != nil && rawProxyURL != "" {
		return nil, errors.New("telegram proxy cannot be combined with a custom HTTP client")
	}
	if custom != nil {
		return custom, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Telegram routing is explicit. Do not inherit process-wide proxy
	// variables and do not silently fall back if a configured proxy fails.
	transport.Proxy = nil
	if rawProxyURL == "" {
		return &http.Client{Transport: transport}, nil
	}
	proxyURL, err := url.Parse(rawProxyURL)
	if err != nil || proxyURL.Host == "" || proxyURL.RawQuery != "" ||
		proxyURL.Fragment != "" || proxyURL.Path != "" || !telegramProxyScheme(proxyURL.Scheme) {
		return nil, errors.New("telegram proxy URL is invalid")
	}
	transport.Proxy = http.ProxyURL(proxyURL)
	return &http.Client{Transport: transport}, nil
}

func telegramProxyScheme(scheme string) bool {
	return scheme == "http" || scheme == "https" || scheme == "socks5" || scheme == "socks5h"
}
