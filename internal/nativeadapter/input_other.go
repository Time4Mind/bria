//go:build !darwin && !linux

package nativeadapter

import "io"

func interruptibleInput(input io.ReadCloser) (io.ReadCloser, error) { return input, nil }
