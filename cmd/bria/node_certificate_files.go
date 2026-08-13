package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/security"
)

func installCredentialFiles(
	nodeConfig config.Config,
	response security.CertificateRenewalResponse,
	privateKeyPEM []byte,
	previous previousNodeCredentials,
) (string, error) {
	parent := filepath.Join(nodeConfig.DataDir, "pki", "renewals")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create renewal directory: %w", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return "", fmt.Errorf("secure renewal directory: %w", err)
	}
	target := filepath.Join(parent, response.RequestID)
	previousJSON, err := json.MarshalIndent(previous, "", "  ")
	if err != nil {
		return "", err
	}
	files := map[string][]byte{
		"node.crt": response.CertificatePEM, "node.key": privateKeyPEM,
		"previous.json": append(previousJSON, '\n'),
	}
	if _, err := os.Stat(target); err == nil {
		if err := verifyCredentialStaging(target, files); err != nil {
			return "", err
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect renewal staging: %w", err)
	}
	if err := writeBundleExclusive(target, files); err != nil {
		return "", err
	}
	return target, nil
}

func verifyCredentialStaging(directory string, files map[string][]byte) error {
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return errors.New("existing renewal directory is unsafe")
	}
	for name, expected := range files {
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(expected)) ||
			(runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
			return fmt.Errorf("existing renewal file %s is unsafe", name)
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != string(expected) {
			return fmt.Errorf("existing renewal file %s does not match request", name)
		}
	}
	return nil
}
