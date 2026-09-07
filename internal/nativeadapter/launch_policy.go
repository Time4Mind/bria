package nativeadapter

import (
	"bria/internal/nativecli"
	"strings"
)

func nativeLaunchPolicy(euid int, environment []string) nativecli.LaunchPolicy {
	sandbox := ""
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "IS_SANDBOX" {
			sandbox = value
		}
	}
	return nativecli.LaunchPolicy{Root: euid == 0, ExistingCLISandbox: sandbox == "1"}
}
