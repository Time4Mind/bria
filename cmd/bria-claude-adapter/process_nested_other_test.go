//go:build !darwin && !linux

package main

import "os"

func runNestedProcessHelper(string) {
	os.Exit(104)
}
