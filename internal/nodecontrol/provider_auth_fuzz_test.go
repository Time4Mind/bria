package nodecontrol

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func FuzzProviderAuthStartRejectsUnauthenticatedInput(f *testing.F) {
	f.Add([]byte(`{"actor_id":7,"node_id":"target","backend":"codex"}`), "application/json")
	f.Add([]byte("not-json"), "text/plain")
	f.Fuzz(func(t *testing.T, body []byte, contentType string) {
		if len(body) > maxControlPayload*2 {
			t.Skip()
		}
		server := &Server{}
		request := httptest.NewRequest(http.MethodPost, providerAuthStartPath, bytes.NewReader(body))
		request.Header.Set("Content-Type", contentType)
		recorder := httptest.NewRecorder()
		server.handleProviderAuthStart(recorder, request)
		if recorder.Code == http.StatusOK {
			t.Fatal("unauthenticated request succeeded")
		}
	})
}
