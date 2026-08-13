//go:build !linux && !darwin

package main

import "os"

func preserveFileOwner(_ *os.File, _ os.FileInfo) error { return nil }
