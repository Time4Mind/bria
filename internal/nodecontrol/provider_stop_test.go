package nodecontrol

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/providerstop"
	"github.com/Time4Mind/bria/internal/security"
)

func TestProviderStopRequiresMatchingMemberIdentity(t *testing.T) {
	state := controlState(t)
	guard, err := NewStateGuard(staticState{state})
	if err != nil {
		t.Fatal(err)
	}
	ca, caPEM, _, err := security.GenerateCA("cluster", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	target := issueTLSCertificate(t, ca, "cluster", "target")
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	bus := providerstop.NewBus(2)
	server, err := NewServer(ServerConfig{
		NodeID: "target", ClusterID: "cluster", Certificate: target.Certificate,
		Roots: roots, Leadership: staticLeadership("target"), Membership: guard,
		Service: &submitRecorder{}, ProviderStops: bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	signal := providerstop.Signal{
		NodeID: "target", SessionID: "s", ProviderSessionID: "provider",
		RuntimeGeneration: 3,
	}
	body, _ := json.Marshal(signal)
	request := httptest.NewRequest(http.MethodPost, providerStopPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: target.LeafCertificate}
	response := httptest.NewRecorder()
	server.handleProviderStop(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("provider stop status=%d body=%q", response.Code, response.Body.String())
	}
	select {
	case got := <-bus.Events():
		if got != signal {
			t.Fatalf("provider stop=%#v", got)
		}
	default:
		t.Fatal("provider stop was not delivered")
	}

	signal.NodeID = "leader"
	body, _ = json.Marshal(signal)
	request = httptest.NewRequest(http.MethodPost, providerStopPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{PeerCertificates: target.LeafCertificate}
	response = httptest.NewRecorder()
	server.handleProviderStop(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("spoofed provider stop status=%d", response.Code)
	}
}
