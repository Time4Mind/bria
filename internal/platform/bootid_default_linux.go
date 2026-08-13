//go:build linux

package platform

import "os"

func NewBootIDProvider() BootIDProvider {
	return NewLinuxBootIDProvider(FileReaderFunc(os.ReadFile))
}
