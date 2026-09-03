package main

import (
	"context"
	"fmt"
	"os"

	"bria/internal/containerpreflight"
)

const (
	providerRoot = "/opt/bria/providers"
	modelRoot    = "/opt/bria/models"
)

func main() {
	if len(os.Args) != 7 || os.Args[1] != "--config" || os.Args[3] != "--lock" || os.Args[5] != "--role" {
		fmt.Fprintln(os.Stderr, "usage: bria-container-preflight --config ABSOLUTE --lock ABSOLUTE --role combined|coordinator|executor")
		os.Exit(2)
	}
	err := containerpreflight.Verify(context.Background(), containerpreflight.Options{
		ConfigPath: os.Args[2], LockPath: os.Args[4], ExpectedRole: os.Args[6],
		ProviderRoot: providerRoot, ModelRoot: modelRoot,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "bria-container-preflight: verification failed")
		os.Exit(78)
	}
	fmt.Fprintln(os.Stdout, "Bria container provider lock: OK")
}
