package platform

import (
	"context"
	"fmt"
	"strings"
)

const linuxBootIDPath = "/proc/sys/kernel/random/boot_id"

type FileReader interface {
	ReadFile(string) ([]byte, error)
}

type FileReaderFunc func(string) ([]byte, error)

func (read FileReaderFunc) ReadFile(path string) ([]byte, error) {
	return read(path)
}

type LinuxBootIDProvider struct {
	reader FileReader
}

func NewLinuxBootIDProvider(reader FileReader) *LinuxBootIDProvider {
	return &LinuxBootIDProvider{reader: reader}
}

func (p *LinuxBootIDProvider) Current(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p == nil || p.reader == nil {
		return "", fmt.Errorf("linux boot id reader is required")
	}
	raw, err := p.reader.ReadFile(linuxBootIDPath)
	if err != nil {
		return "", fmt.Errorf("read linux boot id: %w", err)
	}
	bootID := strings.ToLower(strings.TrimSpace(string(raw)))
	if !isCanonicalUUID(bootID) {
		return "", fmt.Errorf("linux boot id is not a canonical UUID")
	}
	return bootID, nil
}

func isCanonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
