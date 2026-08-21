// Package binaryidentity computes a bounded content identity for Bria executables.
package binaryidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

const MaxExecutableBytes int64 = 512 << 20

func Current() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return SHA256(executable)
}

func SHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > MaxExecutableBytes {
		return "", errors.New("Bria executable identity input is invalid")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, MaxExecutableBytes+1))
	if err != nil {
		return "", err
	}
	after, err := file.Stat()
	if err != nil {
		return "", err
	}
	if written != before.Size() || !os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("Bria executable changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
