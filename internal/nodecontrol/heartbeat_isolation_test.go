package nodecontrol

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestHeartbeatRejectsFalseBackendIsolationClaim(t *testing.T) {
	server, followerCertificate, recorder := heartbeatTestServer(t, "leader")
	report := Heartbeat{
		ReportID: "report", NodeID: "target", BootID: "boot",
		BackendIsolation: domain.BackendIsolationReport{Mode: "trusted", Ready: true},
	}
	request := heartbeatRequest(t, report, followerCertificate)
	response := httptest.NewRecorder()
	server.handleHeartbeat(response, request)
	if response.Code != http.StatusBadRequest || recorder.calls != 0 {
		t.Fatalf("status=%d commits=%d", response.Code, recorder.calls)
	}
}
