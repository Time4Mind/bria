package telegram

import (
	"errors"
	"fmt"
	"net/http"
)

const officialAPIHost = "api.telegram.org"

// NewProductionHTTPClient returns the network client intended for NewClient in
// production. It rejects redirects and refuses every destination except the
// exact official Telegram Bot API TLS host.
func NewProductionHTTPClient() HTTPClient {
	base := http.DefaultTransport
	if transport, ok := base.(*http.Transport); ok && transport != nil {
		direct := transport.Clone()
		direct.Proxy = nil
		base = direct
	}
	return newProductionHTTPClient(base)
}

type productionHTTPClient struct {
	client *http.Client
}

func newProductionHTTPClient(base http.RoundTripper) *productionHTTPClient {
	return &productionHTTPClient{client: &http.Client{
		Transport: officialRoundTripper{base: base},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (client *productionHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if err := validateOfficialRequest(request); err != nil {
		return nil, err
	}
	response, err := client.client.Do(request)
	if err == nil {
		return response, nil
	}
	if request != nil && request.Context() != nil {
		if contextErr := request.Context().Err(); contextErr != nil {
			return nil, fmt.Errorf("Telegram production HTTP request: %w", contextErr)
		}
	}
	return nil, errors.New("Telegram production HTTP request failed")
}

type officialRoundTripper struct {
	base http.RoundTripper
}

func (transport officialRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := validateOfficialRequest(request); err != nil {
		return nil, err
	}
	return transport.base.RoundTrip(request)
}

func validateOfficialRequest(request *http.Request) error {
	if request == nil || request.URL == nil {
		return errors.New("Telegram production HTTP client requires a request URL")
	}
	if request.URL.Scheme != "https" || request.URL.Host != officialAPIHost ||
		request.URL.User != nil || (request.Host != "" && request.Host != officialAPIHost) {
		return errors.New("Telegram production HTTP client rejected a non-official destination")
	}
	return nil
}
