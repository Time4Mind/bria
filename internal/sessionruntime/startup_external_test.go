package sessionruntime_test

import (
	"bria/internal/sessionruntime"
	"context"
	"strings"
	"testing"
	"time"
)

func TestStartupClassFromChildStderrNeverExposesCredentials(t *testing.T) {
	starter := newHelperStarter(t, "startup-diagnostic", sessionruntime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := starter.Start(ctx, testRequest(t.TempDir(), "startup-diagnostic"))
	if err == nil || sessionruntime.StartupFailureClass(err) != "bypass_forbidden" || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "credential") {
		t.Fatal("startup class missing or private diagnostic exposed")
	}
}
