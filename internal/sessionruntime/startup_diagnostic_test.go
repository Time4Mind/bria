package sessionruntime

import (
	"errors"
	"strings"
	"testing"
)

func TestStartupDiagnosticBoundedWhitelist(t *testing.T) {
	for _, input := range []string{
		"token=secret bria-native-startup:bypass_forbidden\n",
		strings.Repeat("x", 513) + "\n",
		strings.Repeat("x", 32<<10) + "\nbria-native-startup:bypass_forbidden\n",
		"bria-native-startup:secret\n",
	} {
		d := &startupDiagnostic{}
		_, _ = d.Write([]byte(input))
		d.finish()
		err := d.wrap(errors.New("startup failed"))
		if StartupFailureClass(err) != "" || err.Error() != "startup failed" {
			t.Fatal("untrusted stderr escaped whitelist")
		}
	}
}
